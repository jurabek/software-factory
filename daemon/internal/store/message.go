package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
)

type Message struct {
	Sequence       int64          `json:"sequence"`
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id"`
	Actor          string         `json:"actor"`
	Text           string         `json:"text"`
	IdempotencyKey string         `json:"idempotency_key"`
	TargetType     string         `json:"-"`
	TargetID       string         `json:"-"`
	Target         *MessageTarget `json:"target,omitempty"`
	StageID        string         `json:"stage_id,omitempty"`
	RecipientRole  string         `json:"recipient_role"`
	AgentSessionID string         `json:"agent_session_id"`
	DeliveryStatus string         `json:"delivery_status"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	CreatedAt      string         `json:"created_at"`
	DeliveredAt    string         `json:"delivered_at,omitempty"`
	FailedAt       string         `json:"failed_at,omitempty"`
}
type MessageTarget struct {
	AttemptID string `json:"attempt_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// AcceptMessageWithEvent stores a new Message, applies the associated Task
// lifecycle changes, and records its history event in one transaction. The
// event trace is derived output and is written only after the commit.

// The database is authoritative; trace export is derived output.

func scanMessage(scanner interface{ Scan(...any) error }) (Message, error) {
	var value Message
	err := scanner.Scan(&value.Sequence, &value.ID, &value.TaskID, &value.Actor, &value.Text, &value.IdempotencyKey, &value.TargetType, &value.TargetID, &value.StageID, &value.RecipientRole, &value.AgentSessionID, &value.DeliveryStatus, &value.FailureReason, &value.CreatedAt, &value.DeliveredAt, &value.FailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err == nil && value.TargetType != "" {
		value.Target = &MessageTarget{}
		switch value.TargetType {
		case "attempt":
			value.Target.AttemptID = value.TargetID
		case "event":
			value.Target.EventID = value.TargetID
		}
	}
	return value, wrap("read message", err)
}

// QueuedMessageForStages reports whether a queued message is routed to any of
// the given stage identifiers.

// BeginMessageInvocationWithEvent delivers a queued message and records its
// delivery event in the same database transaction. The file trace is derived
// output and is written only after the transaction commits.

// DeliverMessageWithEvent marks a queued message delivered and records its
// delivery event without touching the agent-session invocation marker. The
// caller records that marker separately, so a message dispatch and an agent
// invocation that share a transaction window stay distinct.

// FailMessageWithEvent publishes a message failure and its lifecycle event in
// one transaction. The event trace is derived output and is written only
// after the database commit.

type MessageRepository struct{ db *sql.DB }

func (r *MessageRepository) Save(ctx context.Context, value Message) (Message, bool, error) {
	result, err := r.db.ExecContext(ctx, `insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID, value.Actor,
		value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID, value.DeliveryStatus,
		value.CreatedAt)
	if err != nil {
		return Message{},
			false, wrap("save message", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message insert result",
			err)
	}
	stored, err := r.messageByKey(ctx, value.TaskID, value.IdempotencyKey)
	return stored,
		rows == 1, err
}

// AcceptMessageWithEvent stores a new Message, applies the associated Task

// lifecycle changes, and records its history event in one transaction. The

// event trace is derived output and is written only after the commit.

func (r *MessageRepository) AcceptWithEvent(ctx context.Context, value Message, event Event, invalidateApproval, reopen bool, reopenState, taskDir string) (Message, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, wrap("begin message acceptance", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID,
		value.Actor, value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID,
		value.DeliveryStatus, value.CreatedAt)
	if err != nil {
		return Message{}, false, wrap("accept message",
			err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message acceptance result", err)
	}
	stored, err := scanMessage(tx.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`, value.TaskID, value.IdempotencyKey))
	if err != nil {
		return Message{},
			false, err
	}
	if rows != 1 {
		return stored, false, nil
	}
	if invalidateApproval {
		if _, err = tx.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`, value.TaskID); err != nil {
			return Message{}, false, wrap("invalidate approval for message", err)
		}
	}
	if reopen {
		result, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=null where id=?`, reopenState, value.TaskID)
		if err != nil {
			return Message{}, false, wrap("reopen task for message", err)
		}
		if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
			if countErr != nil {
				return Message{}, false, wrap("check task reopen for message", countErr)
			}
			return Message{}, false, ErrConflict
		}
	}
	sequence, err := appendEventTx(ctx, tx, event)
	if err != nil {
		return Message{}, false, err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err != nil {
		return Message{}, false, wrap(
			"commit message acceptance", err)
	}
	if err = writeEventTrace(
		taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			value.TaskID, "event_id",
			event.ID, "error",
			err)
	}
	return stored, true, nil
}

func (r *MessageRepository) messageByKey(ctx context.Context, taskID, key string) (Message, error) {
	return scanMessage(r.db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`, taskID, key))
}

func (r *MessageRepository) ByIdempotencyKey(ctx context.Context, taskID, key string) (Message, error) {
	return r.messageByKey(ctx, taskID, key)
}

func (r *MessageRepository) List(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? order by sequence`, taskID)
	if err != nil {
		return nil, wrap("list messages", err)
	}
	defer rows.Close()
	values := make([]Message, 0)
	for rows.Next() {
		value, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MessageRepository) NextQueued(ctx context.Context, taskID, role string) (Message, error) {
	return scanMessage(r.db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and stage_id=? and delivery_status='queued' order by sequence limit 1`, taskID, role))
}

func (r *MessageRepository) NextQueuedForTask(ctx context.Context, taskID string) (Message, error) {
	return scanMessage(r.db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence limit 1`, taskID))
}

// QueuedMessageForStages reports whether a queued message is routed to any of

// the given stage identifiers.

func (r *MessageRepository) QueuedForStages(ctx context.Context, taskID string, stages ...string) (bool, error) {
	if len(stages) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(stages)), ",")
	args := make([]any, 0, len(stages)+1)
	args = append(args, taskID)
	for _, stage := range stages {
		args = append(args, stage)
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `select count(*) from messages where task_id=? and delivery_status='queued' and stage_id in (`+placeholders+`)`,
		args...).Scan(&count); err != nil {
		return false, wrap("count queued stage messages",
			err)
	}
	return count > 0, nil
}

func (r *MessageRepository) BeginInvocation(ctx context.Context, taskID, role, invocationID, messageID string) error {
	return r.beginMessageInvocation(ctx, taskID, role, invocationID,
		messageID, nil, "")
}

// BeginMessageInvocationWithEvent delivers a queued message and records its

// delivery event in the same database transaction. The file trace is derived

// output and is written only after the transaction commits.

func (r *MessageRepository) BeginInvocationWithEvent(ctx context.Context, taskID, role, invocationID, messageID string, event Event, taskDir string) error {
	return r.beginMessageInvocation(ctx, taskID, role, invocationID, messageID, &event, taskDir)
}

// DeliverMessageWithEvent marks a queued message delivered and records its

// delivery event without touching the agent-session invocation marker. The

// caller records that marker separately, so a message dispatch and an agent

// invocation that share a transaction window stay distinct.

func (r *MessageRepository) DeliverWithEvent(ctx context.Context, taskID, messageID string, event Event, taskDir string) error {
	return r.beginMessageInvocation(ctx, taskID, "", "", messageID, &event, taskDir)
}

func (r *MessageRepository) beginMessageInvocation(ctx context.Context, taskID, role, invocationID, messageID string, event *Event, taskDir string) error {
	tx, err := r.db.BeginTx(ctx,
		nil)
	if err != nil {
		return wrap("begin message delivery",
			err)
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `select state from tasks where id=?`,
		taskID).Scan(&state); err != nil {
		return wrap("read message task state", err)
	}
	if state == "aborted" {
		return ErrConflict
	}
	if role != "" {
		result, err := tx.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID, now(), taskID, role)
		if err != nil {
			return wrap("begin message invocation", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `update messages set delivery_status='delivered',delivered_at=?,failure_reason=null,failed_at=null where task_id=? and id=? and delivery_status='queued'`, now(),
		taskID, messageID)
	if err != nil {
		return wrap("deliver message",
			err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if event != nil {
		sequence, appendErr := appendEventTx(ctx, tx, *event)
		if appendErr != nil {
			return appendErr
		}
		event.Sequence = sequence
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit message delivery",
			err)
	}
	if event != nil {
		if err = writeEventTrace(taskDir, *event, event.Sequence); err != nil {
			slog.Error("write derived event trace", "task_id",
				taskID, "event_id",
				event.ID, "error", err)
		}
	}
	return nil
}

func (r *MessageRepository) Fail(ctx context.Context, taskID, messageID, reason string) (Message, error) {
	return r.failMessage(ctx, taskID, messageID, reason, nil, "")
}

// FailMessageWithEvent publishes a message failure and its lifecycle event in

// one transaction. The event trace is derived output and is written only

// after the database commit.

func (r *MessageRepository) FailWithEvent(ctx context.Context, taskID, messageID, reason string, event Event, taskDir string) (Message, error) {
	return r.failMessage(ctx, taskID, messageID, reason, &event, taskDir)
}

func (r *MessageRepository) failMessage(ctx context.Context, taskID, messageID, reason string, event *Event, taskDir string) (Message, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, wrap("begin fail message",
			err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and id=? and delivery_status in ('queued','delivered')`, reason,
		now(), taskID, messageID)
	if err != nil {
		return Message{},
			wrap("fail message", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Message{}, ErrConflict
	}
	var key string
	if err = tx.QueryRowContext(ctx, `select idempotency_key from messages where task_id=? and id=?`,
		taskID, messageID,
	).Scan(&key); err != nil {
		return Message{}, wrap("read failed message",
			err)
	}
	if event != nil {
		sequence, appendErr := appendEventTx(ctx, tx, *event)
		if appendErr != nil {
			return Message{}, appendErr
		}
		event.Sequence = sequence
	}
	if err = tx.Commit(); err != nil {
		return Message{}, wrap("commit failed message", err)
	}
	stored, err := r.messageByKey(ctx, taskID, key)
	if err != nil {
		return Message{}, err
	}
	if event != nil {
		if err = writeEventTrace(taskDir, *event, event.Sequence); err != nil {
			slog.Error("write derived event trace", "task_id",
				taskID, "event_id", event.ID, "error", err)
		}
	}
	return stored, nil
}

func (r *MessageRepository) FailQueuedForStages(ctx context.Context, taskID, reason string, stages ...string) error {
	if len(stages) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(stages)), ",")
	args := make([]any, 0, len(stages)+3)
	args = append(args, reason, now(), taskID)
	for _, stage := range stages {
		args = append(args, stage)
	}
	_, err := r.db.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and delivery_status='queued' and stage_id in (`+placeholders+`)`, args...)
	return wrap("fail queued stage messages", err)
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
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
	Anchor         string         `json:"-"`
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
	AttemptID  string          `json:"attempt_id,omitempty"`
	EventID    string          `json:"event_id,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
	Anchor     json.RawMessage `json:"anchor,omitempty"`
}

func (db *DB) SaveMessage(ctx context.Context, value Message) (Message, bool, error) {
	result, err := db.ExecContext(ctx, `insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,anchor_json,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID, value.Actor, value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.Anchor), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID, value.DeliveryStatus, value.CreatedAt)
	if err != nil {
		return Message{}, false, wrap("save message", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message insert result", err)
	}
	stored, err := db.messageByKey(ctx, value.TaskID, value.IdempotencyKey)
	return stored, rows == 1, err
}

// CommitMessageAcceptance atomically queues a Message, applies its optional
// Task state change, invalidates approval when required, and appends its event.
func (db *DB) CommitMessageAcceptance(ctx context.Context, value Message, fromState, toState string, invalidateApproval bool, event Event, taskDir string) (Message, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, wrap("begin message acceptance", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,anchor_json,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID, value.Actor, value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.Anchor), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID, value.DeliveryStatus, value.CreatedAt)
	if err != nil {
		return Message{}, false, wrap("save message", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message insert result", err)
	}
	stored, err := scanMessage(tx.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`, value.TaskID, value.IdempotencyKey))
	if err != nil {
		return Message{}, false, err
	}
	var line []byte
	if created == 1 {
		if invalidateApproval {
			if _, err = tx.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`, value.TaskID); err != nil {
				return Message{}, false, wrap("invalidate message approval", err)
			}
		}
		if toState != "" {
			result, updateErr := tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=null,error=null,ended_at=null where id=? and state=?`, toState, value.TaskID, fromState)
			if updateErr != nil {
				return Message{}, false, wrap("schedule message task", updateErr)
			}
			count, countErr := result.RowsAffected()
			if countErr != nil {
				return Message{}, false, wrap("read scheduled message task", countErr)
			}
			if count != 1 {
				return Message{}, false, ErrConflict
			}
		}
		if _, line, err = insertEvent(ctx, tx, event); err != nil {
			return Message{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Message{}, false, wrap("commit message acceptance", err)
	}
	if created == 1 {
		if err = exportEvent(taskDir, line); err != nil {
			log.Printf("event export after committed message acceptance: %v", err)
		}
	}
	return stored, created == 1, nil
}
func (db *DB) messageByKey(ctx context.Context, taskID, key string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`, taskID, key))
}
func (db *DB) MessageByIdempotencyKey(ctx context.Context, taskID, key string) (Message, error) {
	return db.messageByKey(ctx, taskID, key)
}
func scanMessage(scanner interface{ Scan(...any) error }) (Message, error) {
	var value Message
	err := scanner.Scan(&value.Sequence, &value.ID, &value.TaskID, &value.Actor, &value.Text, &value.IdempotencyKey, &value.TargetType, &value.TargetID, &value.Anchor, &value.StageID, &value.RecipientRole, &value.AgentSessionID, &value.DeliveryStatus, &value.FailureReason, &value.CreatedAt, &value.DeliveredAt, &value.FailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err == nil && value.TargetType != "" {
		value.Target = &MessageTarget{Anchor: json.RawMessage(value.Anchor)}
		switch value.TargetType {
		case "attempt":
			value.Target.AttemptID = value.TargetID
		case "event":
			value.Target.EventID = value.TargetID
		case "artifact":
			value.Target.ArtifactID = value.TargetID
		}
		if value.Anchor == "" {
			value.Target.Anchor = nil
		}
	}
	return value, wrap("read message", err)
}
func (db *DB) Messages(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? order by sequence`, taskID)
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
func (db *DB) NextQueuedMessage(ctx context.Context, taskID, role string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and stage_id=? and delivery_status='queued' order by sequence limit 1`, taskID, role))
}
func (db *DB) NextQueuedTaskMessage(ctx context.Context, taskID string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence limit 1`, taskID))
}
func (db *DB) BeginMessageInvocation(ctx context.Context, taskID, role, invocationID, messageID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin message delivery", err)
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `select state from tasks where id=?`, taskID).Scan(&state); err != nil {
		return wrap("read message task state", err)
	}
	if state == "aborted" {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID, now(), taskID, role)
	if err != nil {
		return wrap("begin message invocation", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update messages set delivery_status='delivered',delivered_at=?,failure_reason=null,failed_at=null where task_id=? and id=? and delivery_status='queued'`, now(), taskID, messageID)
	if err != nil {
		return wrap("deliver message", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return wrap("commit message delivery", tx.Commit())
}
func (db *DB) FailMessage(ctx context.Context, taskID, messageID, reason string) (Message, error) {
	result, err := db.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and id=? and delivery_status in ('queued','delivered')`, reason, now(), taskID, messageID)
	if err != nil {
		return Message{}, wrap("fail message", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Message{}, ErrConflict
	}
	var key string
	if err = db.QueryRowContext(ctx, `select idempotency_key from messages where task_id=? and id=?`, taskID, messageID).Scan(&key); err != nil {
		return Message{}, wrap("read failed message", err)
	}
	return db.messageByKey(ctx, taskID, key)
}
func (db *DB) AbortTask(ctx context.Context, taskID, from, activePhase string) ([]Message, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap("begin abort", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence`, taskID)
	if err != nil {
		return nil, wrap("read abort messages", err)
	}
	messages := make([]Message, 0)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		messages = append(messages, message)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, wrap("scan abort messages", err)
	}
	if err = rows.Close(); err != nil {
		return nil, wrap("close abort messages", err)
	}
	timestamp := now()
	result, err := tx.ExecContext(ctx, `update tasks set previous_state=state,state='aborted',active_phase=?,error=null,ended_at=? where id=? and state=?`, nullIfEmpty(activePhase), timestamp, taskID, from)
	if err != nil {
		return nil, wrap("abort task", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason='task_aborted',failed_at=? where task_id=? and delivery_status='queued'`, timestamp, taskID); err != nil {
		return nil, wrap("fail aborted messages", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, wrap("commit abort", err)
	}
	for index := range messages {
		messages[index].DeliveryStatus = "failed"
		messages[index].FailureReason = "task_aborted"
		messages[index].FailedAt = timestamp
	}
	return messages, nil
}

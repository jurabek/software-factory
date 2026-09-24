package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

type AgentSession struct {
	TaskID               string        `json:"task_id"`
	StageID              string        `json:"stage_id"`
	AgentName            string        `json:"agent_name,omitempty"`
	Role                 string        `json:"role"`
	Harness              string        `json:"harness"`
	Provider             string        `json:"provider,omitempty"`
	Model                string        `json:"model,omitempty"`
	Thinking             string        `json:"thinking,omitempty"`
	Color                string        `json:"color,omitempty"`
	HarnessSessionID     string        `json:"harness_session_id"`
	SessionDirectory     string        `json:"session_directory"`
	SessionReady         bool          `json:"session_ready"`
	NativeTranscriptPath string        `json:"native_transcript_path,omitempty"`
	PendingInvocationID  string        `json:"-"`
	PendingRequestID     string        `json:"-"`
	PendingPhaseID       string        `json:"-"`
	ContextTokens        int           `json:"context_tokens,omitempty"`
	ContextWindow        int           `json:"context_window,omitempty"`
	Usage                session.Usage `json:"usage"`
	Cost                 float64       `json:"cost"`
	LastEntryID          string        `json:"last_entry_id,omitempty"`
	CreatedAt            string        `json:"created_at"`
	LastUsedAt           string        `json:"last_used_at"`
}

// PendingAgentSessions lists sessions with an in-flight invocation. The
// reconcile loop resolves each against the native session before settling it.

// ReconcileAgentStats records native-derived usage, cost, context, and leaf for
// an in-flight session without clearing its pending marker. The cost is
// absolute, so re-driving a lost turn stays idempotent.

// ClearAgentInvocation releases a pending marker that no longer maps to a
// usable native turn, leaving the session settled and ready to re-drive.

// RequeueInterruptedPhase returns an in-flight phase to the queue so an
// explicit resume reuses it instead of creating a replacement attempt.

// ResetAgentSession starts a fresh native session for a stage, discarding the
// prior conversation. An exact retry uses it when the source attempt had no
// recorded native checkpoint to fork from. A pending invocation blocks it.

type AgentSessionRepository struct{ db *sql.DB }

func (r *AgentSessionRepository) Reserve(ctx context.Context, taskID string, value AgentSession) (AgentSession, error) {
	timestamp := now()
	stageID := value.StageID
	if stageID == "" {
		stageID = value.Role
	}
	agentName := value.AgentName
	if agentName == "" {
		agentName = value.Role
	}
	_, err := r.db.ExecContext(ctx, `insert into agent_sessions(task_id,stage_id,agent_name,role,harness,provider,model,thinking,color,harness_session_id,session_directory,session_ready,usage_json,cost,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,stage_id) do nothing`, taskID, stageID, agentName, agentName, value.Harness, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), value.HarnessSessionID, value.SessionDirectory, boolToInt(value.SessionReady), `{}`, value.Cost, timestamp, timestamp)
	if err != nil {
		return AgentSession{}, wrap("reserve agent session", err)
	}
	stored, err := r.Get(ctx, taskID, stageID)
	if err != nil {
		return AgentSession{}, err
	}
	if stored.Harness != value.Harness || stored.SessionDirectory != value.SessionDirectory {
		return AgentSession{}, ErrConflict
	}
	return stored, nil
}

func (r *AgentSessionRepository) Get(ctx context.Context, taskID, role string) (AgentSession, error) {
	var value AgentSession
	var usage string
	var ready int
	err := r.db.QueryRowContext(ctx, `select stage_id,agent_name,harness,coalesce(provider,''),coalesce(model,''),coalesce(thinking,''),coalesce(color,''),harness_session_id,session_directory,session_ready,coalesce(native_transcript_path,''),coalesce(pending_invocation_id,''),coalesce(pending_request_id,''),coalesce(pending_phase_id,''),coalesce(context_tokens,0),coalesce(context_window,0),coalesce(usage_json,'{}'),coalesce(cost,0),coalesce(last_entry_id,''),created_at,last_used_at from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&value.StageID, &value.AgentName, &value.Harness, &value.Provider, &value.Model, &value.Thinking, &value.Color, &value.HarnessSessionID, &value.SessionDirectory, &ready, &value.NativeTranscriptPath, &value.PendingInvocationID, &value.PendingRequestID, &value.PendingPhaseID, &value.ContextTokens, &value.ContextWindow, &usage, &value.Cost, &value.LastEntryID, &value.CreatedAt, &value.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	if err != nil {
		return AgentSession{}, wrap("read agent session",
			err)
	}
	value.TaskID = taskID
	value.SessionReady = ready != 0
	value.Role = value.AgentName
	if err := json.Unmarshal([]byte(usage), &value.Usage); err != nil {
		return AgentSession{}, wrap("decode agent session usage",
			err)
	}
	return value, nil
}

func (r *AgentSessionRepository) List(ctx context.Context, taskID string) ([]AgentSession, error) {
	rows, err := r.db.QueryContext(ctx, `select stage_id from agent_sessions where task_id=? order by stage_id`, taskID)
	if err != nil {
		return nil, wrap("list agent sessions", err)
	}
	defer rows.Close()
	roles := make([]string, 0)
	for rows.Next() {
		var stageID string
		if err := rows.Scan(&stageID); err != nil {
			return nil, err
		}
		roles = append(roles, stageID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]AgentSession, 0, len(roles))
	for _, role := range roles {
		value, readErr := r.Get(ctx, taskID, role)
		if readErr != nil {
			return nil, readErr
		}
		values = append(values, value)
	}
	return values, nil
}

func (r *AgentSessionRepository) BeginInvocation(ctx context.Context, taskID, role, invocationID, requestID, phaseID string) error {
	result, err := r.db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,pending_request_id=?,pending_phase_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID,
		nullIfEmpty(requestID), nullIfEmpty(phaseID), now(), taskID, role)
	if err != nil {
		return wrap("begin agent invocation",
			err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation result",
			err)
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (r *AgentSessionRepository) FinalizeInvocation(ctx context.Context, taskID, role, invocationID string, value AgentSession) error {
	usage, err := json.Marshal(value.Usage)
	if err != nil {
		return fmt.Errorf("encode agent session usage: %w",
			err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin agent invocation finalization",
			err)
	}
	defer tx.Rollback()
	var previousCost float64
	if err = tx.QueryRowContext(ctx, `select coalesce(cost,0) from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&previousCost); err != nil {
		return wrap("read agent session cost", err)
	}
	result, err := tx.ExecContext(ctx, `update agent_sessions set provider=?,model=?,thinking=?,color=?,session_ready=?,native_transcript_path=?,last_entry_id=?,context_tokens=?,context_window=?,usage_json=?,cost=?,pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=? and harness_session_id=?`, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), boolToInt(value.SessionReady), nullIfEmpty(value.NativeTranscriptPath), nullIfEmpty(value.LastEntryID), value.ContextTokens, value.ContextWindow,
		string(usage), value.Cost, now(), taskID,
		role, invocationID, value.HarnessSessionID)
	if err != nil {
		return wrap("finalize agent invocation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation finalization", err)
	}
	if count == 0 {
		var pending string
		readErr := tx.QueryRowContext(ctx, `select coalesce(pending_invocation_id,'') from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&pending)
		if readErr != nil {
			return wrap("read pending agent invocation", readErr)
		}
		if pending != "" {
			return ErrConflict
		}
		return nil
	}
	if delta := value.Cost - previousCost; delta != 0 {
		if _, err = tx.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`, delta, taskID); err != nil {
			return wrap("update task agent cost", err)
		}
	}
	return wrap("commit agent invocation", tx.Commit())
}

// PendingAgentSessions lists sessions with an in-flight invocation. The

// reconcile loop resolves each against the native session before settling it.

func (r *AgentSessionRepository) Pending(ctx context.Context) ([]AgentSession, error) {
	rows, err := r.db.QueryContext(ctx, `select task_id,stage_id from agent_sessions where pending_invocation_id is not null order by task_id,stage_id`)
	if err != nil {
		return nil, wrap("list pending agent sessions",
			err)
	}
	defer rows.Close()
	type key struct {
		taskID, stageID string
	}
	keys := make([]key, 0)
	for rows.Next() {
		var value key
		if err := rows.Scan(&value.taskID, &value.stageID); err != nil {
			return nil, err
		}
		keys = append(keys, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]AgentSession, 0, len(keys))
	for _, value := range keys {
		session, readErr := r.Get(ctx, value.taskID, value.stageID)
		if readErr != nil {
			return nil, readErr
		}
		values = append(values, session)
	}
	return values,
		nil
}

// ReconcileAgentStats records native-derived usage, cost, context, and leaf for

// an in-flight session without clearing its pending marker. The cost is

// absolute, so re-driving a lost turn stays idempotent.

func (r *AgentSessionRepository) ReconcileStats(ctx context.Context, taskID, stageID string, value AgentSession) error {
	usage, err := json.Marshal(value.Usage)
	if err != nil {
		return fmt.Errorf("encode agent session usage: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin agent reconcile",
			err)
	}
	defer tx.Rollback()
	var previousCost float64
	if err = tx.QueryRowContext(ctx, `select coalesce(cost,0) from agent_sessions where task_id=? and stage_id=?`,
		taskID,
		stageID).Scan(&previousCost); err != nil {
		return wrap(
			"read agent session cost", err)
	}
	if _, err = tx.ExecContext(
		ctx, `update agent_sessions set provider=?,model=?,native_transcript_path=?,last_entry_id=?,context_tokens=?,context_window=?,usage_json=?,cost=?,last_used_at=? where task_id=? and stage_id=?`, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.NativeTranscriptPath), nullIfEmpty(value.LastEntryID), value.ContextTokens, value.ContextWindow, string(usage), value.Cost, now(), taskID, stageID); err != nil {
		return wrap("reconcile agent session",
			err)
	}
	if delta := value.Cost - previousCost; delta != 0 {
		if _, err = tx.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`,
			delta, taskID); err != nil {
			return wrap("update task agent cost", err)
		}
	}
	return wrap("commit agent reconcile", tx.Commit())
}

// ClearAgentInvocation releases a pending marker that no longer maps to a

// usable native turn, leaving the session settled and ready to re-drive.

func (r *AgentSessionRepository) ClearInvocation(ctx context.Context, taskID, stageID, invocationID string) error {
	result, err := r.db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=?`, now(), taskID, stageID, invocationID)
	if err != nil {
		return wrap("clear agent invocation", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrConflict
	}
	return nil
}

// RequeueInterruptedPhase returns an in-flight phase to the queue so an

// explicit resume reuses it instead of creating a replacement attempt.

func (r *AgentSessionRepository) Reset(ctx context.Context, taskID, stageID, newSessionID string) error {
	result, err := r.db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_ready=0,native_transcript_path=null,last_entry_id=null,pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, newSessionID,
		now(), taskID, stageID)
	if err != nil {
		return wrap("reset agent session", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent session reset",
			err)
	}
	if count == 0 {
		var exists int
		if readErr := r.db.QueryRowContext(ctx, `select count(*) from agent_sessions where task_id=? and stage_id=?`,
			taskID, stageID).Scan(&exists); readErr != nil {
			return wrap("read agent session for reset",
				readErr)
		}
		if exists == 0 {
			return ErrNotFound
		}
		return ErrConflict
	}
	return nil
}

func (r *AgentSessionRepository) Replace(ctx context.Context, taskID, role, newSessionID, newDirectory string) (priorID string, err error) {
	var current AgentSession
	current, err = r.Get(ctx, taskID, role)
	if err != nil {
		return "", err
	}
	if current.PendingInvocationID != "" {
		return "", ErrConflict
	}
	result, err := r.db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_directory=?,session_ready=0,native_transcript_path=null,last_entry_id=null,last_used_at=? where task_id=? and stage_id=? and harness_session_id=? and pending_invocation_id is null`, newSessionID, newDirectory, now(), taskID, role,
		current.HarnessSessionID)
	if err != nil {
		return "",
			wrap("replace agent session", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", wrap("read agent session replacement",
			err)
	}
	if count != 1 {
		return "", ErrConflict
	}
	return current.HarnessSessionID, nil
}

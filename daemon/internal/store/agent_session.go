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
	ContextTokens        int           `json:"context_tokens,omitempty"`
	ContextWindow        int           `json:"context_window,omitempty"`
	Usage                session.Usage `json:"usage"`
	Cost                 float64       `json:"cost"`
	AccountingComplete   bool          `json:"accounting_complete"`
	CreatedAt            string        `json:"created_at"`
	LastUsedAt           string        `json:"last_used_at"`
}

func (db *DB) ReserveAgentSession(ctx context.Context, taskID string, value AgentSession) (AgentSession, error) {
	timestamp := now()
	stageID := value.StageID
	if stageID == "" {
		stageID = value.Role
	}
	agentName := value.AgentName
	if agentName == "" {
		agentName = value.Role
	}
	_, err := db.ExecContext(ctx, `insert into agent_sessions(task_id,stage_id,agent_name,role,harness,provider,model,thinking,color,harness_session_id,session_directory,session_ready,usage_json,cost,accounting_complete,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,stage_id) do nothing`, taskID, stageID, agentName, agentName, value.Harness, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), value.HarnessSessionID, value.SessionDirectory, boolToInt(value.SessionReady), `{}`, value.Cost, boolToInt(value.AccountingComplete), timestamp, timestamp)
	if err != nil {
		return AgentSession{}, wrap("reserve agent session", err)
	}
	stored, err := db.AgentSession(ctx, taskID, stageID)
	if err != nil {
		return AgentSession{}, err
	}
	if stored.Harness != value.Harness || stored.SessionDirectory != value.SessionDirectory {
		return AgentSession{}, ErrConflict
	}
	return stored, nil
}

func (db *DB) AgentSession(ctx context.Context, taskID, role string) (AgentSession, error) {
	var value AgentSession
	var usage string
	var ready, complete int
	err := db.QueryRowContext(ctx, `select stage_id,agent_name,harness,coalesce(provider,''),coalesce(model,''),coalesce(thinking,''),coalesce(color,''),harness_session_id,session_directory,session_ready,coalesce(native_transcript_path,''),coalesce(pending_invocation_id,''),coalesce(context_tokens,0),coalesce(context_window,0),coalesce(usage_json,'{}'),coalesce(cost,0),accounting_complete,created_at,last_used_at from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&value.StageID, &value.AgentName, &value.Harness, &value.Provider, &value.Model, &value.Thinking, &value.Color, &value.HarnessSessionID, &value.SessionDirectory, &ready, &value.NativeTranscriptPath, &value.PendingInvocationID, &value.ContextTokens, &value.ContextWindow, &usage, &value.Cost, &complete, &value.CreatedAt, &value.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	if err != nil {
		return AgentSession{}, wrap("read agent session", err)
	}
	value.SessionReady = ready != 0
	value.Role = value.AgentName
	value.AccountingComplete = complete != 0 && value.PendingInvocationID == ""
	if err := json.Unmarshal([]byte(usage), &value.Usage); err != nil {
		return AgentSession{}, wrap("decode agent session usage", err)
	}
	return value, nil
}

func (db *DB) AgentSessions(ctx context.Context, taskID string) ([]AgentSession, error) {
	rows, err := db.QueryContext(ctx, `select stage_id from agent_sessions where task_id=? order by stage_id`, taskID)
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
		value, readErr := db.AgentSession(ctx, taskID, role)
		if readErr != nil {
			return nil, readErr
		}
		values = append(values, value)
	}
	return values, nil
}

func (db *DB) BeginAgentInvocation(ctx context.Context, taskID, role, invocationID string) error {
	result, err := db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID, now(), taskID, role)
	if err != nil {
		return wrap("begin agent invocation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation result", err)
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) FinalizeAgentInvocation(ctx context.Context, taskID, role, invocationID string, value AgentSession) error {
	usage, err := json.Marshal(value.Usage)
	if err != nil {
		return fmt.Errorf("encode agent session usage: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin agent invocation finalization", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update agent_sessions set provider=?,model=?,thinking=?,color=?,session_ready=?,native_transcript_path=?,context_tokens=?,context_window=?,usage_json=?,cost=cost+?,accounting_complete=accounting_complete and ?,pending_invocation_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=? and harness_session_id=?`, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), boolToInt(value.SessionReady), nullIfEmpty(value.NativeTranscriptPath), value.ContextTokens, value.ContextWindow, string(usage), value.Cost, boolToInt(value.AccountingComplete), now(), taskID, role, invocationID, value.HarnessSessionID)
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
	if _, err = tx.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`, value.Cost, taskID); err != nil {
		return wrap("update task agent cost", err)
	}
	return wrap("commit agent invocation", tx.Commit())
}

func (db *DB) RecoverPendingAgentSessions(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `update agent_sessions set accounting_complete=0,pending_invocation_id=null where pending_invocation_id is not null`)
	return wrap("recover pending agent sessions", err)
}

func (db *DB) ReplaceAgentSession(ctx context.Context, taskID, role, newSessionID, newDirectory string) (priorID string, err error) {
	var current AgentSession
	current, err = db.AgentSession(ctx, taskID, role)
	if err != nil {
		return "", err
	}
	if current.PendingInvocationID != "" {
		return "", ErrConflict
	}
	result, err := db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_directory=?,session_ready=0,native_transcript_path=null,last_used_at=? where task_id=? and stage_id=? and harness_session_id=? and pending_invocation_id is null`, newSessionID, newDirectory, now(), taskID, role, current.HarnessSessionID)
	if err != nil {
		return "", wrap("replace agent session", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", wrap("read agent session replacement", err)
	}
	if count != 1 {
		return "", ErrConflict
	}
	return current.HarnessSessionID, nil
}

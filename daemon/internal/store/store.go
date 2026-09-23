package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if incompatible, inspectErr := incompatibleSchema(context.Background(), db); inspectErr != nil {
		db.Close()
		return nil, fmt.Errorf("inspect database schema: %w", inspectErr)
	} else if incompatible {
		db.Close()
		return nil, ErrStateIncompatible
	}
	if _, err = db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err = ensureRetriableColumns(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	wrapped := &DB{DB: db}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return wrapped, nil
}

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrStaleBranch       = errors.New("stale_branch")
	ErrStateIncompatible = errors.New("state_incompatible: delete the configured Software Factory directory before starting this clean-break version")
)

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
func (db *DB) ReserveAgentSession(ctx context.
	Context, taskID string, value AgentSession) (AgentSession, error) {
	timestamp := now()
	stageID := value.StageID
	if stageID ==
		"" {
		stageID = value.Role
	}
	agentName := value.
		AgentName
	if agentName == "" {
		agentName = value.
			Role
	}
	_, err := db.ExecContext(ctx, `insert into agent_sessions(task_id,stage_id,agent_name,role,harness,provider,model,thinking,color,harness_session_id,session_directory,session_ready,usage_json,cost,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,stage_id) do nothing`,

		taskID, stageID, agentName, agentName, value.
			Harness, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), value.
			HarnessSessionID, value.SessionDirectory, boolToInt(value.SessionReady), `{}`, value.Cost, timestamp, timestamp)
	if err !=
		nil {
		return AgentSession{}, wrap("reserve agent session", err)
	}
	stored,
		err := db.AgentSession(ctx, taskID, stageID)
	if err != nil {
		return AgentSession{}, err
	}
	if stored.Harness != value.Harness || stored.
		SessionDirectory != value.SessionDirectory {
		return AgentSession{}, ErrConflict
	}
	return stored, nil
}
func (db *DB) AgentSession(ctx context.Context, taskID, role string) (AgentSession, error) {
	var value AgentSession
	var usage string
	var ready int
	err := db.QueryRowContext(ctx, `select stage_id,agent_name,harness,coalesce(provider,''),coalesce(model,''),coalesce(thinking,''),coalesce(color,''),harness_session_id,session_directory,session_ready,coalesce(native_transcript_path,''),coalesce(pending_invocation_id,''),coalesce(pending_request_id,''),coalesce(pending_phase_id,''),coalesce(context_tokens,0),coalesce(context_window,0),coalesce(usage_json,'{}'),coalesce(cost,0),coalesce(last_entry_id,''),created_at,last_used_at from agent_sessions where task_id=? and stage_id=?`,

		taskID, role,
	).Scan(&value.StageID, &value.AgentName, &value.Harness, &value.
		Provider, &value.Model, &value.Thinking, &value.Color, &value.
		HarnessSessionID, &value.SessionDirectory, &ready, &value.
		NativeTranscriptPath, &value.PendingInvocationID, &value.PendingRequestID, &value.
		PendingPhaseID, &value.ContextTokens, &value.
		ContextWindow, &usage, &value.Cost, &value.LastEntryID,
		&value.CreatedAt, &value.LastUsedAt)
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
func (db *DB) AgentSessions(ctx context.Context, taskID string) ([]AgentSession, error) {
	rows, err := db.QueryContext(ctx, `select stage_id from agent_sessions where task_id=? order by stage_id`,

		taskID)
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
func (db *DB) BeginAgentInvocation(ctx context.
	Context, taskID,
	role,
	invocationID,

	requestID, phaseID string) error {
	result, err := db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,pending_request_id=?,pending_phase_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`,

		invocationID,
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
func (db *DB) FinalizeAgentInvocation(ctx context.Context,
	taskID, role,
	invocationID string, value AgentSession) error {
	usage, err := json.
		Marshal(value.Usage)
	if err !=
		nil {
		return fmt.Errorf("encode agent session usage: %w",
			err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin agent invocation finalization",
			err)
	}
	defer tx.Rollback()
	var previousCost float64
	if err = tx.QueryRowContext(ctx, `select coalesce(cost,0) from agent_sessions where task_id=? and stage_id=?`,

		taskID, role).Scan(&previousCost); err != nil {
		return wrap("read agent session cost", err)
	}
	result, err := tx.ExecContext(ctx, `update agent_sessions set provider=?,model=?,thinking=?,color=?,session_ready=?,native_transcript_path=?,last_entry_id=?,context_tokens=?,context_window=?,usage_json=?,cost=?,pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=? and harness_session_id=?`,

		nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), boolToInt(value.SessionReady), nullIfEmpty(value.NativeTranscriptPath), nullIfEmpty(value.LastEntryID), value.ContextTokens, value.ContextWindow,
		string(usage), value.Cost, now(), taskID,
		role, invocationID, value.HarnessSessionID)
	if err != nil {
		return wrap("finalize agent invocation", err)
	}
	count,
		err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation finalization", err)
	}
	if count ==
		0 {
		var pending string
		readErr := tx.QueryRowContext(ctx, `select coalesce(pending_invocation_id,'') from agent_sessions where task_id=? and stage_id=?`,

			taskID, role).Scan(&pending)
		if readErr !=
			nil {
			return wrap("read pending agent invocation",

				readErr)
		}
		if pending !=
			"" {
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

func (db *DB) PendingAgentSessions(ctx context.Context) ([]AgentSession, error) {
	rows, err := db.QueryContext(ctx, `select task_id,stage_id from agent_sessions where pending_invocation_id is not null order by task_id,stage_id`)
	if err !=
		nil {
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
		if err := rows.Scan(&value.taskID, &value.stageID); err !=
			nil {
			return nil, err
		}
		keys = append(keys, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]AgentSession, 0, len(keys))
	for _, value := range keys {
		session, readErr := db.AgentSession(ctx, value.
			taskID, value.stageID)
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
func (db *DB) ReconcileAgentStats(ctx context.Context, taskID, stageID string, value AgentSession) error {
	usage, err := json.Marshal(value.Usage)
	if err != nil {
		return fmt.Errorf("encode agent session usage: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
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
		ctx, `update agent_sessions set provider=?,model=?,native_transcript_path=?,last_entry_id=?,context_tokens=?,context_window=?,usage_json=?,cost=?,last_used_at=? where task_id=? and stage_id=?`,

		nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.NativeTranscriptPath), nullIfEmpty(value.LastEntryID), value.ContextTokens, value.
			ContextWindow, string(usage), value.Cost, now(), taskID, stageID); err !=
		nil {
		return wrap("reconcile agent session",
			err)
	}
	if delta := value.Cost - previousCost; delta !=
		0 {
		if _, err = tx.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`,
			delta, taskID); err != nil {
			return wrap("update task agent cost", err)
		}
	}
	return wrap("commit agent reconcile", tx.Commit())
}

// ClearAgentInvocation releases a pending marker that no longer maps to a

// usable native turn, leaving the session settled and ready to re-drive.

func (db *DB) ClearAgentInvocation(ctx context.Context, taskID, stageID,
	invocationID string) error {
	result, err := db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=?`,

		now(), taskID, stageID, invocationID)
	if err !=
		nil {
		return wrap("clear agent invocation", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrConflict
	}
	return nil
}

// RequeueInterruptedPhase returns an in-flight phase to the queue so an

// explicit resume reuses it instead of creating a replacement attempt.

func (db *DB) RequeueInterruptedPhase(ctx context.Context, taskID, phaseID string) error {
	_, err := db.ExecContext(ctx, `update phases set status='queued',error=null,ended_at=null where task_id=? and id=? and status='interrupted'`,

		taskID, phaseID,
	)
	return wrap("requeue interrupted phase", err)
}

// ResetAgentSession starts a fresh native session for a stage, discarding the

// prior conversation. An exact retry uses it when the source attempt had no

// recorded native checkpoint to fork from. A pending invocation blocks it.

func (db *DB) ResetAgentSession(ctx context.
	Context, taskID, stageID, newSessionID string) error {
	result, err := db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_ready=0,native_transcript_path=null,last_entry_id=null,pending_invocation_id=null,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`,

		newSessionID,
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
		if readErr := db.QueryRowContext(ctx, `select count(*) from agent_sessions where task_id=? and stage_id=?`,
			taskID, stageID).Scan(&exists); readErr != nil {
			return wrap("read agent session for reset",
				readErr)
		}
		if exists ==
			0 {
			return ErrNotFound
		}
		return ErrConflict
	}
	return nil
}
func (db *DB) ReplaceAgentSession(ctx context.
	Context, taskID,
	role,
	newSessionID,

	newDirectory string) (priorID string, err error) {
	var current AgentSession
	current,
		err = db.AgentSession(ctx, taskID, role)
	if err !=
		nil {
		return "", err
	}
	if current.PendingInvocationID !=
		"" {
		return "", ErrConflict
	}
	result, err := db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_directory=?,session_ready=0,native_transcript_path=null,last_entry_id=null,last_used_at=? where task_id=? and stage_id=? and harness_session_id=? and pending_invocation_id is null`,

		newSessionID, newDirectory, now(), taskID, role,
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
func (db *DB) CreateBranch(ctx context.Context, branch Branch) error {
	_,
		err := db.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`,

		branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), nullIfEmpty(branch.ForkAttemptID), nullIfEmpty(branch.HeadAttemptID), branch.Status, branch.
			CreatedAt, branch.CreatedAt)
	return wrap("create branch",
		err)
}
func (db *DB) Branches(ctx context.Context, taskID string) ([]Branch, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? order by created_at`,

		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Branch,
		0)
	for rows.Next() {
		var value Branch
		if err =
			rows.Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.
				ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.
				CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) Branch(ctx context.Context,
	taskID, branchID string) (
	Branch,

	error) {
	var value Branch
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? and id=?`,

		taskID, branchID).Scan(&value.
		ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID,
		&value.HeadAttemptID, &value.Status, &value.
			CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, ErrNotFound
	}
	return value, wrap("read branch",
		err)
}
func (db *DB) SetBranchHead(ctx context.Context, taskID, branchID,
	headAttemptID string) error {
	_, err := db.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`,

		nullIfEmpty(headAttemptID), now(), taskID, branchID)
	return wrap("move branch head",
		err)
}
func (db *DB) SelectBranch(ctx context.Context, taskID, branchID string) error {
	_, err := db.ExecContext(ctx, `update tasks set selected_branch_id=? where id=?`,

		nullIfEmpty(branchID), taskID)
	return wrap("select branch",
		err)
}
func (db *DB) TaskHeadAttempt(ctx context.
	Context, taskID string) string {

	var selected string
	_ = db.QueryRowContext(ctx, `select coalesce(selected_branch_id,'') from tasks where id=?`,

		taskID).Scan(&selected)
	if selected ==
		"" {
		return ""
	}
	var head string
	_ = db.QueryRowContext(ctx, `select coalesce(head_attempt_id,'') from branches where task_id=? and id=?`,
		taskID, selected).Scan(&head)
	return head
}
func (db *DB) SaveCheck(ctx context.Context, check Check) error {
	_,
		err :=

		db.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,output_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,

			check.ID, check.TaskID,
			nullIfEmpty(check.PhaseID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.ComparisonBaseline), check.Name, check.Command, check.Attempt, check.Status,
			check.ExitCode, check.Output, check.OutputPath, check.DurationMS,
			check.StartedAt, check.EndedAt)
	return wrap("save check",
		err)
}
func (db *DB) Checks(ctx context.Context,
	taskID string) (
	[]Check, error) {

	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),coalesce(stage_id,''),coalesce(check_phase,'primary'),coalesce(comparison_baseline,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(output_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where task_id=? order by rowid`,

		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.TaskID, &value.
			PhaseID, &value.StageID, &value.Phase, &value.ComparisonBaseline,
			&value.Name, &value.Command, &value.Attempt, &value.Status,
			&value.ExitCode, &value.Output, &value.OutputPath, &value.DurationMS,
			&value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.
		Err()
}
func (db *DB) CreateDefinition(ctx context.
	Context, definition PhaseDefinition) error {
	_, err := db.ExecContext(ctx, `insert into phase_definitions(id,task_id,phase_key,revision,executor,owner,spec_json,digest,parent_revision,created_at) values(?,?,?,?,?,?,?,?,?,?)`,

		definition.
			ID, definition.TaskID, definition.PhaseKey, definition.Revision,
		definition.Executor, definition.Owner, definition.
			Spec, definition.Digest, definition.ParentRevision,
		definition.CreatedAt)
	return wrap("create phase definition",
		err)
}
func (db *DB) LatestDefinition(ctx context.
	Context, taskID,
	phaseKey string) (PhaseDefinition, error) {
	var value PhaseDefinition
	err :=
		db.
			QueryRowContext(ctx, `select id,task_id,phase_key,revision,coalesce(executor,''),coalesce(owner,''),coalesce(spec_json,'{}'),coalesce(digest,''),coalesce(parent_revision,0),created_at from phase_definitions where task_id=? and phase_key=? order by revision desc limit 1`,

				taskID, phaseKey).Scan(&value.ID,
			&value.TaskID, &value.PhaseKey, &value.Revision, &value.Executor,
			&value.Owner, &value.Spec, &value.Digest, &value.ParentRevision,
			&value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseDefinition{}, ErrNotFound
	}
	return value, wrap("read phase definition", err)
}
func (db *DB) SaveEnvelope(ctx context.Context, id, taskID,
	phaseID,
	role,
	outputType,
	payload string, valid bool, attempt int) error {
	_,
		err := db.ExecContext(ctx, `insert into envelopes(id,task_id,phase_id,stage_id,agent_role,output_type,payload_json,valid,attempt,created_at) values(?,?,?,?,?,?,?,?,?,?)`,

		id, taskID, phaseID, role, role,
		outputType, payload, valid, attempt, now())
	if err == nil && valid && role == "planner" {
		digest :=
			fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
		_, err = db.
			ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`,
				digest, taskID)
	}
	return wrap("save envelope", err)
}
func (db *DB) Envelopes(ctx context.Context, taskID string) ([]Envelope, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),coalesce(stage_id,''),agent_role,output_type,payload_json,valid,attempt,created_at from envelopes where task_id=? order by created_at`,

		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Envelope, 0)
	for rows.Next() {
		var value Envelope
		if err := rows.
			Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.StageID,
				&value.AgentRole, &value.OutputType, &value.Payload, &value.
					Valid, &value.Attempt, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values,
		rows.Err()
}
func (db *DB) ValidEnvelope(ctx context.Context, taskID, role string) (string, error) {
	var payload string
	err := db.QueryRowContext(ctx,
		`select payload_json from envelopes where task_id=? and agent_role=? and valid=1 order by created_at desc limit 1`,

		taskID, role).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return payload, wrap("read envelope", err)
}
func (db *DB) AppendEvent(ctx context.Context, taskDir string, event Event) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin event append: %w",
			err)
	}
	defer tx.Rollback()
	sequence, err := appendEventTx(ctx, tx, event)
	if err !=
		nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit event: %w", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			event.TaskID, "event_id", event.ID, "error", err)
	}
	return sequence, nil
}
func (db *DB) Events(ctx context.Context,
	taskID string, after int64, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err :=

		db.
			QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and sequence>? order by sequence limit ?`,

				taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (db *DB) RecentEvents(ctx context.Context, taskID string, limit int) (
	[]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,phase_id,parent_event_id,kind,format_version,name,native_entry_id,request_id,payload_json,display_json,token_count,started_at,ended_at,attempt_id,branch_id,actions_json from (select sequence,id,task_id,coalesce(phase_id,'') as phase_id,coalesce(parent_event_id,'') as parent_event_id,kind,format_version,coalesce(name,'') as name,coalesce(native_entry_id,'') as native_entry_id,coalesce(request_id,'') as request_id,payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,'') as attempt_id,coalesce(branch_id,'') as branch_id,coalesce(actions_json,'[]') as actions_json from events where task_id=? order by sequence desc limit ?) order by sequence`,

		taskID, limit)
	if err !=
		nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (db *DB) EventByID(ctx context.Context, taskID, eventID string) (Event,

	error) {
	var event Event
	var payload, display, started, actions string
	var ended sql.NullString
	err := db.QueryRowContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and id=?`,

		taskID, eventID).Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.
		ParentEventID, &event.Kind, &event.FormatVersion, &event.
		Name, &event.NativeEntryID, &event.RequestID, &payload, &display,
		&event.TokenCount, &started, &ended, &event.AttemptID, &event.
			BranchID, &actions)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, wrap("read event",
			err)
	}
	if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
		return Event{}, wrap("decode event payload", err)
	}
	if err := json.Unmarshal([]byte(display), &event.Display); err != nil {
		return Event{}, wrap("decode event display", err)
	}
	_ = json.
		Unmarshal([]byte(actions), &event.AvailableActions)
	event.StartedAt, _ = time.Parse(time.RFC3339Nano,
		started)
	if ended.Valid {
		value, _ := time.Parse(time.RFC3339Nano,
			ended.String)
		event.EndedAt = &value
	}
	return event,
		nil
}

// EventsByRequest returns the agent-derived events recorded for a factory

// request in append order.
func (db *DB) EventsByRequest(ctx context.
	Context, taskID, requestID string) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and request_id=? order by sequence`,

		taskID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// SetEventNativeEntries backfills native entry references for a request's

// events. It is the write half of the reference index; payloads are resolved

// from the native session at read time.
func (db *DB) SetEventNativeEntries(ctx context.Context, taskID string, links []EventNativeLink) error {
	if len(links) == 0 {
		return nil
	}
	tx,
		err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin native link: %w", err)
	}
	defer tx.Rollback()
	for _, link := range links {
		if link.EventID ==
			"" || link.NativeEntryID == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `update events set native_entry_id=? where task_id=? and id=?`,
			link.NativeEntryID, taskID, link.EventID,
		); err != nil {
			return fmt.Errorf("link native entry: %w",
				err)
		}
	}
	return wrap("commit native links", tx.Commit())
}
func (db *DB) SaveTestChanges(ctx context.
	Context, changes []TestChange) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin test-change evidence",
			err)
	}
	defer tx.Rollback()
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `insert or replace into test_changes(id,task_id,phase_id,attempt,path,reason,change_kind,rename_from,rename_to,created_at) values(?,?,?,?,?,?,?,?,?,?)`,

			change.ID, change.TaskID,
			change.PhaseID, change.Attempt, change.Path, change.Reason,
			change.ChangeKind, nullIfEmpty(change.RenameFrom), nullIfEmpty(change.RenameTo), change.CreatedAt); err != nil {
			return wrap("save test-change evidence", err)
		}
	}
	return wrap("commit test-change evidence",
		tx.Commit())
}
func (db *DB) TestChanges(ctx context.Context, taskID string) ([]TestChange,

	error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,phase_id,attempt,path,reason,change_kind,coalesce(rename_from,''),coalesce(rename_to,''),created_at from test_changes where task_id=? order by created_at,rowid`,

		taskID)
	if err != nil {
		return nil, wrap("read test-change evidence", err)
	}
	defer rows.
		Close()
	values := make([]TestChange, 0)
	for rows.
		Next() {
		var value TestChange
		if err := rows.Scan(&value.ID,
			&value.TaskID, &value.PhaseID, &value.Attempt, &value.Path,
			&value.Reason, &value.ChangeKind, &value.RenameFrom, &value.RenameTo,
			&value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) SaveComparison(ctx context.
	Context, value Comparison) error {

	overlay, err := json.Marshal(value.OverlayPaths)
	if err != nil {
		return wrap("encode comparison overlay paths",
			err)
	}
	_, err = db.ExecContext(ctx, `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?)`,

		value.ID, value.TaskID, value.PhaseID, value.Attempt,
		value.Status, value.Reason, nullIfEmpty(value.BaselineSnapshot), string(overlay), value.CreatedAt, value.DurationMS)
	return wrap("save comparison", err)
}
func (db *DB) Comparisons(ctx context.Context, taskID string) ([]Comparison,

	error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,phase_id,attempt,status,reason,coalesce(baseline_snapshot,''),overlay_paths_json,created_at,duration_ms from comparisons where task_id=? order by created_at,rowid`,

		taskID)
	if err != nil {
		return nil,
			wrap("read comparisons", err)
	}
	defer rows.Close()
	values := make([]Comparison, 0)
	for rows.Next() {
		var value Comparison
		var overlay string
		if err := rows.
			Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt,
				&value.Status, &value.Reason, &value.BaselineSnapshot, &overlay,
				&value.CreatedAt, &value.DurationMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(overlay), &value.
			OverlayPaths); err != nil {
			return nil, wrap("decode comparison overlay paths",
				err)
		}
		values = append(values,
			value)
	}
	return values, rows.Err()
}
func (db *DB) SaveMessage(ctx context.Context, value Message) (Message, bool, error) {
	result, err := db.ExecContext(ctx, `insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`,

		value.ID, value.TaskID, value.Actor,
		value.Text, value.IdempotencyKey, nullIfEmpty(value.
			TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID, value.DeliveryStatus,
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
	stored, err := db.messageByKey(ctx, value.TaskID, value.IdempotencyKey)
	return stored,
		rows == 1, err
}

// AcceptMessageWithEvent stores a new Message, applies the associated Task

// lifecycle changes, and records its history event in one transaction. The

// event trace is derived output and is written only after the commit.

func (db *DB) AcceptMessageWithEvent(ctx context.Context, value Message, event Event, invalidateApproval, reopen bool,
	reopenState, taskDir string) (Message, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, wrap("begin message acceptance", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`,

		value.ID, value.TaskID,
		value.Actor, value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID,
		value.DeliveryStatus, value.CreatedAt)
	if err !=
		nil {
		return Message{}, false, wrap("accept message",
			err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message acceptance result", err)
	}
	stored, err := scanMessage(tx.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`,

		value.TaskID, value.IdempotencyKey,
	))
	if err !=
		nil {
		return Message{},
			false, err
	}
	if rows !=
		1 {
		return stored,

			false, nil
	}
	if invalidateApproval {
		if _, err = tx.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`,

			value.TaskID); err != nil {
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
				return Message{}, false, wrap("check task reopen for message",

					countErr)
			}
			return Message{}, false, ErrConflict
		}
	}
	sequence, err := appendEventTx(ctx, tx, event)
	if err !=
		nil {
		return Message{}, false, err
	}
	event.
		Sequence = sequence
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
func (db *DB) messageByKey(ctx context.Context, taskID, key string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`,

		taskID, key))
}
func (db *DB) MessageByIdempotencyKey(ctx context.Context,
	taskID, key string) (Message, error) {
	return db.messageByKey(ctx, taskID, key)
}
func (db *DB) Messages(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? order by sequence`,

		taskID)
	if err !=
		nil {
		return nil, wrap("list messages", err)
	}
	defer rows.Close()
	values := make([]Message, 0)
	for rows.Next() {
		value,
			scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) NextQueuedMessage(ctx context.
	Context, taskID,
	role string) (
	Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and stage_id=? and delivery_status='queued' order by sequence limit 1`,

		taskID, role,
	))
}
func (db *DB) NextQueuedTaskMessage(ctx context.
	Context, taskID string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence limit 1`,

		taskID))
}

// QueuedMessageForStages reports whether a queued message is routed to any of

// the given stage identifiers.
func (db *DB) QueuedMessageForStages(ctx context.Context,
	taskID string, stages ...string) (bool, error) {
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
	if err := db.QueryRowContext(ctx, `select count(*) from messages where task_id=? and delivery_status='queued' and stage_id in (`+
		placeholders+`)`,
		args...).Scan(&count); err != nil {
		return false, wrap("count queued stage messages",
			err)
	}
	return count > 0, nil
}
func (db *DB) BeginMessageInvocation(ctx context.Context,
	taskID, role,
	invocationID,

	messageID string) error {
	return db.beginMessageInvocation(ctx, taskID, role, invocationID,
		messageID, nil, "")
}

// BeginMessageInvocationWithEvent delivers a queued message and records its

// delivery event in the same database transaction. The file trace is derived

// output and is written only after the transaction commits.
func (
	db *DB) BeginMessageInvocationWithEvent(ctx context.Context, taskID, role, invocationID, messageID string, event Event,
	taskDir string) error {
	return db.beginMessageInvocation(ctx, taskID, role, invocationID, messageID, &event, taskDir)
}

// DeliverMessageWithEvent marks a queued message delivered and records its

// delivery event without touching the agent-session invocation marker. The

// caller records that marker separately, so a message dispatch and an agent

// invocation that share a transaction window stay distinct.
func (db *DB) DeliverMessageWithEvent(ctx context.Context, taskID, messageID string, event Event, taskDir string) error {
	return db.beginMessageInvocation(ctx, taskID, "", "", messageID, &event, taskDir)
}
func (db *DB) beginMessageInvocation(ctx context.Context,
	taskID, role,
	invocationID,

	messageID string, event *Event, taskDir string) error {

	tx, err := db.BeginTx(ctx,
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
	if state ==
		"aborted" {
		return ErrConflict
	}
	if role != "" {
		result,
			err := tx.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,pending_request_id=null,pending_phase_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`,

			invocationID, now(), taskID, role)
		if err !=
			nil {
			return wrap("begin message invocation", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `update messages set delivery_status='delivered',delivered_at=?,failure_reason=null,failed_at=null where task_id=? and id=? and delivery_status='queued'`,

		now(),
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
		if appendErr !=
			nil {
			return appendErr
		}
		event.Sequence = sequence
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit message delivery",
			err)
	}
	if event != nil {
		if err =
			writeEventTrace(taskDir, *event, event.Sequence); err != nil {
			slog.Error("write derived event trace", "task_id",
				taskID, "event_id",
				event.ID, "error", err)
		}
	}
	return nil
}
func (db *DB) FailMessage(ctx context.Context, taskID, messageID,
	reason string) (Message, error) {
	return db.failMessage(ctx, taskID, messageID,

		reason, nil, "")
}

// FailMessageWithEvent publishes a message failure and its lifecycle event in

// one transaction. The event trace is derived output and is written only

// after the database commit.
func (db *DB) FailMessageWithEvent(ctx context.Context, taskID, messageID, reason string, event Event, taskDir string) (Message, error) {
	return db.failMessage(ctx, taskID, messageID, reason, &event, taskDir)
}
func (db *DB) failMessage(ctx context.Context, taskID, messageID,
	reason string, event *Event, taskDir string) (Message, error) {
	tx, err :=

		db.BeginTx(ctx, nil)
	if err !=
		nil {
		return Message{}, wrap("begin fail message",
			err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and id=? and delivery_status in ('queued','delivered')`,

		reason,
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
	stored, err := db.messageByKey(ctx, taskID, key)
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
func (db *DB) AbortTask(ctx context.Context, taskID, from,
	activePhase string) ([]Message, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err !=
		nil {
		return nil, wrap(
			"begin abort", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence`,

		taskID)
	if err != nil {
		return nil, wrap("read abort messages",
			err)
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
	result, err := tx.ExecContext(
		ctx, `update tasks set previous_state=state,state='aborted',active_phase=?,error=null,ended_at=? where id=? and state=?`,
		nullIfEmpty(activePhase), timestamp, taskID, from)
	if err != nil {
		return nil, wrap("abort task", err)
	}
	if count,
		_ := result.RowsAffected(); count != 1 {
		return nil, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason='task_aborted',failed_at=? where task_id=? and delivery_status='queued'`,

		timestamp, taskID); err != nil {
		return nil, wrap("fail aborted messages",
			err)
	}
	if err = tx.Commit(); err != nil {
		return nil, wrap("commit abort",

			err)
	}
	for index := range messages {
		messages[index].DeliveryStatus =
			"failed"
		messages[index].FailureReason = "task_aborted"
		messages[index].FailedAt = timestamp
	}
	return messages, nil
}

// EnqueueOrchestrationEvent records a command after its durable task mutation.

func (db *DB) EnqueueOrchestrationEvent(ctx context.Context,

	event OrchestrationEvent) error {
	_, err := db.ExecContext(ctx, `insert into orchestration_events(id,task_id,type,created_at) values(?,?,?,?)`,

		event.ID, event.TaskID, event.Type, now())
	return wrap(
		"enqueue orchestration event", err)
}
func (db *DB) OrchestrationEvent(ctx context.
	Context, id string) (OrchestrationEvent, error) {
	var event OrchestrationEvent
	err := db.QueryRowContext(ctx, `select id,task_id,type from orchestration_events where id=?`,
		id,
	).Scan(&event.ID, &event.TaskID, &event.Type)
	if err ==
		sql.ErrNoRows {
		return OrchestrationEvent{}, ErrNotFound
	}
	return event, wrap("read orchestration event", err)
}
func (db *DB) PendingOrchestrationEvents(
	ctx context.Context) ([]OrchestrationEvent, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,type from orchestration_events where status='pending' order by created_at,id`)
	if err != nil {
		return nil, wrap("list pending orchestration events",
			err)
	}
	defer rows.Close()
	values := []OrchestrationEvent{}
	for rows.Next() {
		var event OrchestrationEvent
		if err = rows.Scan(&event.ID, &event.TaskID, &event.Type); err != nil {
			return nil, fmt.Errorf("scan orchestration event: %w", err)
		}
		values = append(values, event)
	}
	return values, rows.Err()
}
func (db *DB) CompleteOrchestrationEvent(
	ctx context.Context, id string, cause error) error {
	status, message := "handled", ""
	if cause !=

		nil {
		status, message = "pending",
			cause.Error()
	}
	_, err := db.ExecContext(ctx,
		`update orchestration_events set status=?,error=?,handled_at=? where id=?`,
		status, nullIfEmpty(message), now(), id)
	return wrap("complete orchestration event", err)
}

// StartPhaseWithEvent creates a running phase, updates its Task's active phase

// and branch head, and records the phase-start event atomically.

func (db *DB) StartPhaseWithEvent(ctx context.Context, taskDir string, phase Phase, taskState string, event Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase start",
			err)
	}
	defer tx.Rollback()
	started := phase.StartedAt
	if started == "" {
		started = now()
	}
	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,

		phase.ID, phase.TaskID, phase.
			Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description,
		"running", phase.Attempt, phase.Retries, started, nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.
			InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded), nullIfEmpty(phase.
			NativeBaseEntryID), boolToInt(phase.ForkNative)); err != nil {
		return wrap("insert phase", err)
	}
	if phase.BranchID !=
		"" {
		if _, err = tx.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`,

			phase.ID, now(), phase.TaskID, phase.BranchID,
		); err != nil {
			return wrap("update phase branch head", err)
		}
	}
	result, err := tx.ExecContext(ctx, `update tasks set active_phase=? where id=? and state=?`,
		phase.ID, phase.
			TaskID,

		taskState)
	if err != nil {
		return wrap("set active phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	sequence,
		err := appendEventTx(ctx, tx, event)
	if err !=
		nil {
		return err
	}
	event.
		Sequence = sequence
	if err = tx.Commit(); err !=
		nil {
		return wrap("commit phase start", err)
	}
	if err = writeEventTrace(taskDir, event, event.Sequence); err != nil {
		slog.Error("write derived event trace", "task_id", phase.TaskID, "event_id", event.ID, "error", err)
	}
	return nil
}

// EndPhaseWithEvent publishes a phase result and its lifecycle event

// atomically. Task progression, when needed, uses the transition variant.

func (db *DB) EndPhaseWithEvent(ctx context.Context, taskDir string, phaseID,
	status, message, outputSnapshot string, event Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.
		Rollback()
	result, err := tx.ExecContext(ctx, `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and status='running'`,

		status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), now(), phaseID)
	if err != nil {
		return wrap("complete phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	sequence, err := appendEventTx(ctx, tx, event)
	if err != nil {
		return err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err !=
		nil {
		return wrap("commit phase completion", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			event.TaskID, "event_id", event.ID, "error",
			err)
	}
	return nil
}
func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_,
		err :=

		db.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,

			phase.ID, phase.TaskID, phase.Sequence, phase.Name,
			phase.Kind, phase.Owner, phase.Description, phase.
				Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.
				BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.
				InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.
				Superseded), nullIfEmpty(phase.NativeBaseEntryID), boolToInt(phase.ForkNative))
	return wrap("start phase", err)
}
func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`,

		status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

// CompletePhaseWithTransitionAndEvent publishes a phase result, its Task

// transition, and the corresponding lifecycle event atomically. The event

// trace is derived output and is written only after the database commit.

func (db *DB) CompletePhaseWithTransitionAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status,
	message, outputSnapshot string, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message,
		outputSnapshot, nil, nil, event)
}

// CompletePlannerPhaseWithApproval publishes a planner result and approval

// candidate together with the phase transition and event.
func (
	db *DB) CompletePlannerPhaseWithApproval(ctx context.Context, taskDir string, phaseID,
	taskID, from, to, status, message, outputSnapshot,
	approval string, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID,
		taskID, from, to, status, message, outputSnapshot,
		nil, nil, event, approval)
}

// CompleteVerificationPhaseWithEvidenceAndEvent publishes verification

// evidence, the phase result, the Task transition, and the lifecycle event in

// one transaction. Individual check observations may already be durable; the

// final publication is the authoritative successful verification boundary.
func (db *DB) CompleteVerificationPhaseWithEvidenceAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message,
	outputSnapshot string, checks []Check, comparisons []Comparison, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status,
		message, outputSnapshot, checks, comparisons, event)
}
func (db *DB) completePhaseWithEvidenceAndTransitionAndEvent(ctx context.
	Context, taskDir, phaseID, taskID, from, to, status, message, outputSnapshot string, checks []Check, comparisons []Comparison, event Event, approval ...string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	for _, check := range checks {
		if _, err = tx.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,output_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,

			check.ID, check.TaskID, nullIfEmpty(check.PhaseID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.
				ComparisonBaseline), check.Name, check.Command, check.Attempt,
			check.Status, check.ExitCode, check.Output, check.OutputPath,
			check.DurationMS, check.StartedAt, check.EndedAt); err != nil {
			return wrap("publish verification check", err)
		}
	}
	for _, comparison := range comparisons {
		overlay,
			marshalErr := json.Marshal(comparison.OverlayPaths)
		if marshalErr != nil {
			return wrap("encode verification comparison",
				marshalErr)
		}
		if _, err = tx.ExecContext(ctx, `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?)`,

			comparison.
				ID, comparison.TaskID, comparison.PhaseID, comparison.
				Attempt, comparison.Status, comparison.Reason, nullIfEmpty(comparison.BaselineSnapshot), string(overlay), comparison.CreatedAt, comparison.DurationMS); err != nil {
			return wrap("publish verification comparison",
				err,
			)
		}
	}
	ended :=
		now()
	result, err := tx.
		ExecContext(ctx, `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and task_id=? and status='running'`,

			status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), ended, phaseID, taskID)
	if err != nil {
		return wrap("complete phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`, to, nullIfEmpty(phaseID), nullIfEmpty(message), nullIfTerminalState(to, ended), taskID, from)
	if err != nil {
		return wrap("advance task after phase", err)
	}
	if count, _ := result.
		RowsAffected(); count != 1 {
		return ErrConflict
	}
	if len(approval) > 0 &&
		approval[0] != "" {
		if _, err = tx.ExecContext(ctx,
			`update tasks set plan_digest=? where id=?`,
			approval[0], taskID); err != nil {
			return wrap("save approval candidate", err)
		}
	}
	sequence,
		err := appendEventTx(ctx, tx, event)
	if err != nil {
		return err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err != nil {
		return wrap("commit phase completion",
			err,
		)
	}
	if err = writeEventTrace(
		taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id", taskID, "event_id", event.ID, "error", err)
	}
	return nil
}
func (db *DB) StartQueuedPhase(ctx context.
	Context, taskID,
	phaseID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin queued phase start",
			err)
	}
	defer tx.Rollback()
	result, err := tx.
		ExecContext(ctx, `update phases set status='running',started_at=?,ended_at=null,error=null where task_id=? and id=? and status='queued'`,

			now(), taskID, phaseID)
	if err !=
		nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update tasks set active_phase=? where id=?`,
		phaseID, taskID)
	if err != nil {
		return wrap("set queued phase active", err)
	}
	if count,
		_ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit queued phase start",
			err)
	}
	return nil
}
func (db *DB) Phases(ctx context.Context,
	taskID string) (
	[]Phase, error) {

	rows, err := db.QueryContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0),coalesce(native_base_entry_id,''),coalesce(fork_native,0) from phases where task_id=? order by sequence`,

		taskID)
	if err !=
		nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		var superseded,
			forkNative int
		if err := rows.Scan(&value.ID, &value.
			TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner,
			&value.Description, &value.Status, &value.Attempt, &value.Retries,
			&value.Error, &value.StartedAt, &value.EndedAt, &value.
				BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded, &value.NativeBaseEntryID, &forkNative,
		); err != nil {
			return nil, err
		}
		value.
			Superseded = superseded != 0
		value.ForkNative = forkNative != 0
		values = append(values, value)
	}
	return values,
		rows.Err()
}
func (db *DB) PhaseByID(ctx context.Context, taskID, phaseID string) (Phase,

	error) {
	var value Phase
	var superseded, forkNative int
	err :=

		db.QueryRowContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0),coalesce(native_base_entry_id,''),coalesce(fork_native,0) from phases where task_id=? and id=?`,

			taskID, phaseID).Scan(&value.ID,
			&value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.
				Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.
				OutputSnapshot, &superseded, &value.NativeBaseEntryID, &forkNative)
	if errors.Is(err, sql.ErrNoRows) {
		return Phase{}, ErrNotFound
	}
	value.Superseded = superseded != 0
	value.ForkNative = forkNative != 0
	return value, wrap("read phase", err)
}

// SetPhaseNativeBase records the native session checkpoint an attempt started

// from. It only fills an empty checkpoint so a forked retry keeps the source

// attempt's recorded boundary.
func (db *DB) SetPhaseNativeBase(
	ctx context.Context, taskID, phaseID, entryID string) error {
	if entryID == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `update phases set native_base_entry_id=? where task_id=? and id=? and coalesce(native_base_entry_id,'')=''`,

		entryID, taskID, phaseID)
	return wrap("set phase native base",
		err)
}
func (db *DB) MarkSuperseded(ctx context.
	Context, taskID,
	branchID string,

	keepID string) error {
	_, err := db.ExecContext(ctx, `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`,

		taskID,
		branchID, keepID)
	return wrap("mark superseded",
		err)
}
func (db *DB) StartProcess(ctx context.Context, taskID, phaseID,
	kind,
	name string, pid int, command string) (int64, error) {
	result, err :=

		db.ExecContext(ctx, `insert into processes(task_id,phase_id,kind,name,pid,display_command,status,started_at) values(?,?,?,?,?,?,?,?)`,

			taskID, nullIfEmpty(phaseID), kind, name, pid, command, "running",
			now())
	if err != nil {
		return 0, fmt.Errorf("start process: %w",
			err)
	}
	return result.LastInsertId()
}
func (db *DB) EndProcess(
	ctx context.Context, taskID string, pid, exitCode int) error {
	_, err := db.ExecContext(ctx, `update processes set status=case when ?=0 then 'ended' else 'failed' end,exit_code=?,ended_at=? where task_id=? and pid=? and status='running'`,

		exitCode, exitCode,
		now(), taskID, pid)
	return wrap("end process", err)
}
func (db *DB) Recover(ctx context.Context) error {
	tx, err := db.BeginTx(ctx,

		nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ended := now()
	if _, err = tx.
		ExecContext(ctx, `update processes set status='failed',ended_at=? where status='running'`,
			ended); err !=
		nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update phases set status='interrupted',error='server restarted during active phase',ended_at=? where status in ('running','queued')`,

		ended); err != nil {
		return err
	}
	if _,
		err = tx.ExecContext(ctx, `update branches set status='blocked',updated_at=? where task_id in (select id from tasks where state in ('preparing','planning','building','checking','reviewing')) and status='active'`,

		ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state='blocked',error='server restarted during active phase',ended_at=? where state in ('preparing','planning','building','checking','reviewing')`,

		ended); err != nil {
		return err
	}
	return tx.Commit()
}
func (db *DB) ApplyRetry(
	ctx context.Context, key string,
	branch Branch, phase Phase, nextState string) (RetryResult, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return RetryResult{}, false, err
	}
	defer tx.Rollback()
	var existing RetryResult
	err = tx.
		QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`,

			phase.TaskID, key).Scan(&existing.
		SourceAttemptID, &existing.BranchID, &existing.AttemptID,
		&existing.CreatedAt)
	if err == nil {
		return existing, false,
			nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, false, wrap("read retry request", err)
	}
	if _, err =
		tx.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`,

			branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), branch.ForkAttemptID, phase.ID, branch.Status,
			branch.CreatedAt, branch.CreatedAt); err !=
		nil {
		return RetryResult{}, false, wrap("create retry branch",
			err)
	}
	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,

		phase.ID, phase.TaskID, phase.Sequence, phase.Name,
		phase.Kind, phase.Owner, phase.Description, phase.Status,
		phase.Attempt, phase.Retries, now(), phase.BranchID,
		nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), nullIfEmpty(phase.
			NativeBaseEntryID,
		), boolToInt(phase.ForkNative)); err != nil {
		return RetryResult{},
			false,
			wrap("queue retry attempt",
				err)
	}
	if _, err = tx.ExecContext(ctx, `update tasks set selected_branch_id=?,previous_state=state,state=?,active_phase=?,ended_at=null,error=null where id=?`,

		branch.ID, nextState, phase.ID, phase.TaskID); err != nil {
		return RetryResult{}, false, wrap("select retry branch", err)
	}
	createdAt := now()
	if _, err = tx.ExecContext(ctx, `insert into retry_requests(task_id,idempotency_key,source_attempt_id,branch_id,attempt_id,created_at) values(?,?,?,?,?,?)`, phase.TaskID, key, branch.ForkAttemptID, branch.ID, phase.ID, createdAt); err != nil {
		return RetryResult{}, false, wrap("save retry request", err)
	}
	if err = tx.Commit(); err != nil {
		return RetryResult{}, false, wrap("commit retry", err)
	}
	return RetryResult{SourceAttemptID: branch.ForkAttemptID,
		BranchID: branch.
			ID, AttemptID: phase.ID,
		CreatedAt: createdAt}, true, nil
}
func (db *DB) RetryByIdempotencyKey(ctx context.
	Context, taskID,
	key string) (RetryResult, error) {
	var value RetryResult
	err := db.QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`,

		taskID,
		key).Scan(&value.SourceAttemptID, &value.BranchID, &value.AttemptID,
		&value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, ErrNotFound
	}
	return value, wrap("read retry request", err)
}
func (db *DB) SaveSnapshot(ctx context.Context, snapshot WorkspaceSnapshot) error {
	manifest := snapshot.Manifest
	if manifest == "" {
		manifest = "{}"
	}
	_, err := db.
		ExecContext(ctx, `insert or ignore into workspace_snapshots(digest,task_id,path,size_bytes,manifest_json,created_at) values(?,?,?,?,?,?)`,

			snapshot.Digest, snapshot.TaskID,
			snapshot.Path, snapshot.SizeBytes, manifest, snapshot.
				CreatedAt)
	return wrap("save snapshot", err)
}
func (db *DB) Snapshot(ctx context.Context, digest string) (WorkspaceSnapshot, error) {
	var value WorkspaceSnapshot
	err := db.QueryRowContext(ctx, `select digest,task_id,coalesce(path,''),coalesce(size_bytes,0),coalesce(manifest_json,'{}'),created_at from workspace_snapshots where digest=?`,

		digest).Scan(&value.Digest, &value.TaskID, &value.
		Path, &value.SizeBytes, &value.Manifest, &value.
		CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceSnapshot{}, ErrNotFound
	}
	return value, wrap("read snapshot",
		err)
}
func (db *DB) CreateTask(
	ctx context.Context, task Task) error {
	return db.
		createTask(ctx, task, false)
}
func (db *DB) CreateActiveTask(ctx context.
	Context, task Task) error {
	return db.createTask(ctx, task, true)
}
func (db *DB) createTask(
	ctx context.Context, task Task, requireAvailableSlot bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {

		return fmt.Errorf("begin create task: %w",
			err)
	}
	defer tx.Rollback()
	query := `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

	if requireAvailableSlot {
		query = `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) select ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? where not exists(select 1 from tasks where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing'))`

	}
	result, err := tx.ExecContext(ctx, query, task.
		ID, nullIfEmpty(task.ParentTaskID), task.Request,
		task.WorkspacePath, task.RepositoryType,
		task.RepositorySource, nullIfEmpty(task.SubmittedRepositoryPath), task.State, nullIfEmpty(task.Pipeline), nullIfEmpty(task.ActiveStage), nullIfEmpty(task.ConfigSnapshot), task.CreatedAt,
		nullIfEmpty(task.StartedAt), task.CodingAgent, task.
			Model, task.Thinking)
	if err != nil {
		return wrap("create task", err)
	}
	if requireAvailableSlot {
		count, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return wrap("check task creation", rowsErr)
		}
		if count !=
			1 {
			return ErrConflict
		}
	}
	return wrap("commit task", tx.Commit())
}
func (db *DB) Task(ctx context.
	Context, id string) (Task,
	error) {
	value,
		err := scanTask(db.QueryRowContext(ctx, `select `+taskColumns+` from tasks where id=?`,

		id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, wrap("read task",
			err)
	}
	return value, nil
}
func (db *DB) Tasks(ctx context.
	Context) ([]Task, error) {
	rows, err := db.
		QueryContext(ctx, `select `+taskColumns+` from tasks order by created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	values := make([]Task, 0)
	for rows.Next() {
		value, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan task: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) TaskSessions(ctx context.Context, taskID string) ([]Task, error) {
	var parentTaskID string
	err := db.QueryRowContext(ctx, `select coalesce(parent_task_id,'') from tasks where id=?`,

		taskID).Scan(&parentTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil,
			ErrNotFound
	}
	if err != nil {
		return nil, wrap("read task root",
			err)
	}
	if parentTaskID != "" {
		taskID = parentTaskID
	}
	rows, err := db.QueryContext(ctx, `select `+
		taskColumns+` from tasks where id=? or parent_task_id=? order by created_at`,
		taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task sessions: %w", err)
	}
	defer rows.Close()
	values := make([]Task, 0)
	for rows.Next() {
		value,
			scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, fmt.
				Errorf("scan task session: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) TaskSessionsWithAgents(ctx context.Context,
	taskID string) ([]TaskSession, error) {
	tasks, err := db.TaskSessions(ctx, taskID)

	if err != nil {
		return nil,
			err
	}
	values := make([]TaskSession, 0, len(tasks))
	for _, task := range tasks {
		agents, agentsErr := db.AgentSessions(ctx, task.ID)
		if agentsErr != nil {
			return nil, agentsErr
		}
		values = append(values, TaskSession{Task: task, AgentSessions: agents})
	}
	return values, nil
}
func (db *DB) Transition(
	ctx context.Context, id, from, to,
	activePhase,
	message string) error {
	ended := any(nil)
	if to == "completed" ||

		to ==
			"blocked" || to == "aborted" {
		ended = now()
	}
	result, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`,
		to,
		nullIfEmpty(activePhase), nullIfEmpty(message), ended,
		id, from)
	if err != nil {
		return fmt.Errorf(
			"transition task: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}
func (db *DB) SetPrepared(ctx context.Context, id, repositoryPath,
	snapshot string) error {
	_, err := db.ExecContext(ctx, `update tasks set repository_path=?,config_snapshot=? where id=?`,

		repositoryPath, snapshot, id)
	return wrap("save task workspace", err)
}
func (db *DB) SetApproval(ctx context.Context, id, digest,
	actor string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=? where id=?`,

		digest, actor, now(), id)
	return wrap("save approval", err)
}

// ApproveWithEvent publishes approval metadata, the transition into building,

// and its lifecycle event in one transaction.
func (db *DB) ApproveWithEvent(ctx context.
	Context, taskDir, id, digest, actor string, event Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err !=
		nil {
		return wrap("begin approval", err)
	}
	defer tx.Rollback()
	approvedAt := now()
	result, err := tx.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=?,previous_state=state,state='building' where id=? and state='awaiting_plan_approval'`,

		digest, actor, approvedAt,
		id)
	if err != nil {
		return wrap("save approval", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if event.FormatVersion == 0 {
		event.FormatVersion = session.FormatVersion
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	sequence, err :=
		appendEventTx(ctx, tx, event)
	if err != nil {
		return err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err != nil {
		return wrap("commit approval", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			id, "event_id", event.ID, "error", err)
		return nil
	}
	return nil
}
func (db *DB) SetApprovalCandidate(ctx context.
	Context, id,
	digest string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`,

		digest, id)
	return wrap("save approval candidate",
		err)
}
func (db *DB) SetActiveStage(ctx context.
	Context, taskID,
	stageID string) error {
	_, err := db.ExecContext(ctx, `update tasks set active_stage=? where id=?`,

		nullIfEmpty(stageID), taskID)
	return wrap("save active stage",
		err)
}
func (db *DB) InvalidateApproval(ctx context.
	Context, id string) error {
	_,

		err := db.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`,

		id)
	return wrap("invalidate approval",
		err)
}
func (db *DB) ReopenTask(
	ctx context.Context, taskID, state string) error {

	_, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=null where id=?`,

		state, taskID)
	return wrap("reopen task",
		err)
}
func (db *DB) DeleteTask(
	ctx context.Context, id string) error {
	result,
		err := db.ExecContext(ctx, `delete from tasks where id=?`, id)
	if err !=
		nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// FailedPhaseCount counts failed phases for a stage name.
func (db *DB) FailedPhaseCount(ctx context.Context, taskID, stage string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from phases where task_id=? and name=? and status='failed'`, taskID, stage).Scan(&count); err != nil {
		return 0, wrap("count failed phases", err)
	}
	return count, nil
}

// FailQueuedStageMessages fails queued messages for the given stages.
func (db *DB) FailQueuedStageMessages(ctx context.Context, taskID, reason string, stages ...string) error {
	if len(stages) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(stages)), ",")
	args := make([]any, 0, len(stages)+3)
	args = append(args, reason, now(), taskID)
	for _, stage := range stages {
		args = append(args, stage)
	}
	_, err := db.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and delivery_status='queued' and stage_id in (`+placeholders+`)`, args...)
	return wrap("fail queued stage messages", err)
}

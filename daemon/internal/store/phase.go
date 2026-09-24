package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
)

type Phase struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	StageID        string `json:"stage_id,omitempty"`
	Kind           string `json:"kind"`
	Owner          string `json:"owner"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Sequence       int    `json:"sequence"`
	Attempt        int    `json:"attempt"`
	Retries        int    `json:"retries"`
	BranchID       string `json:"branch_id,omitempty"`
	DefinitionID   string `json:"definition_id,omitempty"`
	DefinitionRev  int    `json:"definition_revision,omitempty"`
	InputSnapshot  string `json:"input_snapshot,omitempty"`
	OutputSnapshot string `json:"output_snapshot,omitempty"`
	Superseded     bool   `json:"superseded,omitempty"`
	// NativeBaseEntryID is the native session leaf immediately before this
	// attempt's first invocation. It is the checkpoint an exact retry forks at.
	NativeBaseEntryID string `json:"native_base_entry_id,omitempty"`
	// ForkNative records that this attempt must branch the native session at
	// NativeBaseEntryID instead of continuing the current leaf.
	ForkNative bool   `json:"fork_native,omitempty"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
}

// StartPhaseWithEvent creates a running phase, updates its Task's active phase
// and branch head, and records the phase-start event atomically.

// EndPhaseWithEvent publishes a phase result and its lifecycle event
// atomically. Task progression, when needed, uses the transition variant.

// CompletePhaseWithTransitionAndEvent publishes a phase result, its Task
// transition, and the corresponding lifecycle event atomically. The event
// trace is derived output and is written only after the database commit.

// CompletePlannerPhaseWithApproval publishes a planner result and approval
// candidate together with the phase transition and event.

// CompleteVerificationPhaseWithEvidenceAndEvent publishes verification
// evidence, the phase result, the Task transition, and the lifecycle event in
// one transaction. Individual check observations may already be durable; the
// final publication is the authoritative successful verification boundary.

func nullIfTerminalState(state string, ended string) any {
	if state == "completed" || state == "blocked" || state == "aborted" {
		return ended
	}
	return nil
}

// SetPhaseNativeBase records the native session checkpoint an attempt started
// from. It only fills an empty checkpoint so a forked retry keeps the source
// attempt's recorded boundary.

type PhaseRepository struct{ db *sql.DB }

func (r *PhaseRepository) RequeueInterrupted(ctx context.Context, taskID, phaseID string) error {
	query := `update phases set status='queued',error=null,ended_at=null where task_id=? and id=? and status='interrupted'`
	_, err := r.db.ExecContext(ctx, query, taskID, phaseID)
	return wrap("requeue interrupted phase", err)
}

// ResetAgentSession starts a fresh native session for a stage, discarding the

// prior conversation. An exact retry uses it when the source attempt had no

// recorded native checkpoint to fork from. A pending invocation blocks it.

func (r *PhaseRepository) StartWithEvent(ctx context.Context, taskDir string, phase Phase, taskState string, event Event) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase start",
			err)
	}
	defer tx.Rollback()
	started := phase.StartedAt
	if started == "" {
		started = now()
	}
	query := `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if _, err = tx.ExecContext(ctx, query, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description,
		"running", phase.Attempt, phase.Retries, started, nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded), nullIfEmpty(phase.NativeBaseEntryID), boolToInt(phase.ForkNative)); err != nil {
		return wrap("insert phase", err)
	}
	if phase.BranchID != "" {
		query2 := `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`
		if _, err = tx.ExecContext(ctx, query2, phase.ID, now(), phase.TaskID, phase.BranchID); err != nil {
			return wrap("update phase branch head", err)
		}
	}
	query3 := `update tasks set active_phase=? where id=? and state=?`
	result, err := tx.ExecContext(ctx, query3,
		phase.ID, phase.TaskID, taskState)
	if err != nil {
		return wrap("set active phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	sequence, err := appendEventTx(ctx, tx, event)
	if err != nil {
		return err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err != nil {
		return wrap("commit phase start", err)
	}
	if err = writeEventTrace(taskDir, event, event.Sequence); err != nil {
		slog.Error("write derived event trace", "task_id", phase.TaskID, "event_id", event.ID, "error", err)
	}
	return nil
}

// EndPhaseWithEvent publishes a phase result and its lifecycle event

// atomically. Task progression, when needed, uses the transition variant.

func (r *PhaseRepository) EndWithEvent(ctx context.Context, taskDir string, phaseID, status, message, outputSnapshot string, event Event) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	query := `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and status='running'`
	result, err := tx.ExecContext(ctx, query, status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), now(), phaseID)
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
	if err = tx.Commit(); err != nil {
		return wrap("commit phase completion", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			event.TaskID, "event_id", event.ID, "error",
			err)
	}
	return nil
}

func (r *PhaseRepository) Add(ctx context.Context, phase Phase) error {
	query := `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, query, phase.ID, phase.TaskID, phase.Sequence, phase.Name,
		phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded), nullIfEmpty(phase.NativeBaseEntryID), boolToInt(phase.ForkNative))
	return wrap("start phase", err)
}

func (r *PhaseRepository) End(ctx context.Context, id, status, message string) error {
	query := `update phases set status=?,error=?,ended_at=? where id=?`
	_, err := r.db.ExecContext(ctx, query, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

// CompletePhaseWithTransitionAndEvent publishes a phase result, its Task

// transition, and the corresponding lifecycle event atomically. The event

// trace is derived output and is written only after the database commit.

func (r *PhaseRepository) CompleteWithTransitionAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message, outputSnapshot string, event Event) error {
	return r.completeWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message,
		outputSnapshot, nil, nil, event)
}

// CompletePlannerPhaseWithApproval publishes a planner result and approval

// candidate together with the phase transition and event.

func (r *PhaseRepository) CompletePlannerWithApproval(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message, outputSnapshot, approval string, event Event) error {
	return r.completeWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID,
		taskID, from, to, status, message, outputSnapshot,
		nil, nil, event, approval)
}

// CompleteVerificationPhaseWithEvidenceAndEvent publishes verification

// evidence, the phase result, the Task transition, and the lifecycle event in

// one transaction. Individual check observations may already be durable; the

// final publication is the authoritative successful verification boundary.

func (r *PhaseRepository) CompleteVerificationWithEvidenceAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message, outputSnapshot string, checks []Check, comparisons []Comparison, event Event) error {
	return r.completeWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status,
		message, outputSnapshot, checks, comparisons, event)
}

func (r *PhaseRepository) completeWithEvidenceAndTransitionAndEvent(ctx context.Context, taskDir, phaseID, taskID, from, to, status, message, outputSnapshot string, checks []Check, comparisons []Comparison, event Event, approval ...string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	for _, check := range checks {
		query := `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,output_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
		if _, err = tx.ExecContext(ctx, query, check.ID, check.TaskID, nullIfEmpty(check.PhaseID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.ComparisonBaseline), check.Name, check.Command, check.Attempt,
			check.Status, check.ExitCode, check.Output, check.OutputPath,
			check.DurationMS, check.StartedAt, check.EndedAt); err != nil {
			return wrap("publish verification check", err)
		}
	}
	for _, comparison := range comparisons {
		overlay, marshalErr := json.Marshal(comparison.OverlayPaths)
		if marshalErr != nil {
			return wrap("encode verification comparison",
				marshalErr)
		}
		query2 := `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?)`
		if _, err = tx.ExecContext(ctx, query2, comparison.ID, comparison.TaskID, comparison.PhaseID, comparison.Attempt, comparison.Status, comparison.Reason, nullIfEmpty(comparison.BaselineSnapshot), string(overlay), comparison.CreatedAt, comparison.DurationMS); err != nil {
			return wrap("publish verification comparison",
				err,
			)
		}
	}
	ended := now()
	query3 := `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and task_id=? and status='running'`
	result, err := tx.ExecContext(ctx, query3, status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), ended, phaseID, taskID)
	if err != nil {
		return wrap("complete phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	query4 := `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`
	result, err = tx.ExecContext(ctx, query4, to, nullIfEmpty(phaseID), nullIfEmpty(message), nullIfTerminalState(to, ended), taskID, from)
	if err != nil {
		return wrap("advance task after phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if len(approval) > 0 && approval[0] != "" {
		query5 := `update tasks set plan_digest=? where id=?`
		if _, err = tx.ExecContext(ctx,
			query5,
			approval[0], taskID); err != nil {
			return wrap("save approval candidate", err)
		}
	}
	sequence, err := appendEventTx(ctx, tx, event)
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

func (r *PhaseRepository) StartQueued(ctx context.Context, taskID, phaseID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin queued phase start",
			err)
	}
	defer tx.Rollback()
	query := `update phases set status='running',started_at=?,ended_at=null,error=null where task_id=? and id=? and status='queued'`
	result, err := tx.ExecContext(ctx, query, now(), taskID, phaseID)
	if err != nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	query2 := `update tasks set active_phase=? where id=?`
	result, err = tx.ExecContext(ctx, query2,
		phaseID, taskID)
	if err != nil {
		return wrap("set queued phase active", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit queued phase start",
			err)
	}
	return nil
}

func (r *PhaseRepository) List(ctx context.Context, taskID string) ([]Phase, error) {
	query := `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0),coalesce(native_base_entry_id,''),coalesce(fork_native,0) from phases where task_id=? order by sequence`
	rows, err := r.db.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		var superseded,
			forkNative int
		if err := rows.Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded, &value.NativeBaseEntryID, &forkNative); err != nil {
			return nil, err
		}
		value.Superseded = superseded != 0
		value.ForkNative = forkNative != 0
		values = append(values, value)
	}
	return values,
		rows.Err()
}

func (r *PhaseRepository) ByID(ctx context.Context, taskID, phaseID string) (Phase, error) {
	var value Phase
	var superseded, forkNative int
	query := `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0),coalesce(native_base_entry_id,''),coalesce(fork_native,0) from phases where task_id=? and id=?`
	err := r.db.QueryRowContext(ctx, query, taskID, phaseID).Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded, &value.NativeBaseEntryID, &forkNative)
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

func (r *PhaseRepository) SetNativeBase(ctx context.Context, taskID, phaseID, entryID string) error {
	if entryID == "" {
		return nil
	}
	query := `update phases set native_base_entry_id=? where task_id=? and id=? and coalesce(native_base_entry_id,'')=''`
	_, err := r.db.ExecContext(ctx, query, entryID, taskID, phaseID)
	return wrap("set phase native base",
		err)
}

// SetOutputSnapshot records the workspace snapshot a phase produced.
func (r *PhaseRepository) SetOutputSnapshot(ctx context.Context, phaseID, snapshot string) error {
	query := `update phases set output_snapshot=? where id=?`
	_, err := r.db.ExecContext(ctx, query,
		snapshot, phaseID)
	return wrap("save phase output snapshot", err)
}

func (r *PhaseRepository) MarkSuperseded(ctx context.Context, taskID, branchID string, keepID string) error {
	query := `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`
	_, err := r.db.ExecContext(ctx, query, taskID,
		branchID, keepID)
	return wrap("mark superseded",
		err)
}

func (r *PhaseRepository) FailedCount(ctx context.Context, taskID, stage string) (int, error) {
	var count int
	query := `select count(*) from phases where task_id=? and name=? and status='failed'`
	if err := r.db.QueryRowContext(ctx, query, taskID, stage).Scan(&count); err != nil {
		return 0, wrap("count failed phases", err)
	}
	return count, nil
}

// FailQueuedStageMessages fails queued messages for the given stages.

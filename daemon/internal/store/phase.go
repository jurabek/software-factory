package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

type Phase struct {
	ID             string `db:"id" json:"id"`
	TaskID         string `db:"task_id" json:"task_id"`
	Name           string `db:"name" json:"name"`
	StageID        string `db:"stage_id" json:"stage_id,omitempty"`
	Kind           string `db:"kind" json:"kind"`
	Owner          string `db:"owner" json:"owner"`
	Description    string `db:"description" json:"description"`
	Status         string `db:"status" json:"status"`
	Error          string `db:"error" json:"error,omitempty"`
	Sequence       int    `db:"sequence" json:"sequence"`
	Attempt        int    `db:"attempt" json:"attempt"`
	Retries        int    `db:"retries" json:"retries"`
	BranchID       string `db:"branch_id" json:"branch_id,omitempty"`
	DefinitionID   string `db:"definition_id" json:"definition_id,omitempty"`
	DefinitionRev  int    `db:"definition_revision" json:"definition_revision,omitempty"`
	InputSnapshot  string `db:"input_snapshot" json:"input_snapshot,omitempty"`
	OutputSnapshot string `db:"output_snapshot" json:"output_snapshot,omitempty"`
	Superseded     bool   `db:"superseded" json:"superseded,omitempty"`
	// NativeBaseEntryID is the native session leaf immediately before this
	// attempt's first invocation. It is the checkpoint an exact retry forks at.
	NativeBaseEntryID string `db:"native_base_entry_id" json:"native_base_entry_id,omitempty"`
	// ForkNative records that this attempt must branch the native session at
	// NativeBaseEntryID instead of continuing the current leaf.
	ForkNative bool   `db:"fork_native" json:"fork_native,omitempty"`
	StartedAt  string `db:"started_at" json:"started_at"`
	EndedAt    string `db:"ended_at" json:"ended_at,omitempty"`
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

type PhaseRepository struct{ db *sqlx.DB }

func (r *PhaseRepository) RequeueInterrupted(ctx context.Context, taskID, phaseID string) error {
	query := `update phases set status='queued',error=null,ended_at=null where task_id=:task_id and id=:id and status='interrupted'`
	_, err := r.db.NamedExecContext(ctx, query, Phase{ID: phaseID, TaskID: taskID})
	return wrap("requeue interrupted phase", err)
}

// ResetAgentSession starts a fresh native session for a stage, discarding the

// prior conversation. An exact retry uses it when the source attempt had no

// recorded native checkpoint to fork from. A pending invocation blocks it.

func (r *PhaseRepository) StartWithEvent(ctx context.Context, taskDir string, phase Phase, taskState string, event Event) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin phase start",
			err)
	}
	defer tx.Rollback()
	started := phase.StartedAt
	if started == "" {
		started = now()
	}
	phase.Status = "running"
	phase.StartedAt = started
	query := `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(:id,:task_id,:sequence,:name,:kind,:owner,:description,:status,:attempt,:retries,:started_at,nullif(:branch_id,''),nullif(:definition_id,''),nullif(:input_snapshot,''),nullif(:output_snapshot,''),:superseded,nullif(:native_base_entry_id,''),:fork_native)`
	if _, err = tx.NamedExecContext(ctx, query, phase); err != nil {
		return wrap("insert phase", err)
	}
	if phase.BranchID != "" {
		query2 := `update branches set head_attempt_id=:head_attempt_id,updated_at=:updated_at where task_id=:task_id and id=:id`
		if _, err = tx.NamedExecContext(ctx, query2, Branch{ID: phase.BranchID, TaskID: phase.TaskID, HeadAttemptID: phase.ID, UpdatedAt: now()}); err != nil {
			return wrap("update phase branch head", err)
		}
	}
	query3 := `update tasks set active_phase=:active_phase where id=:id and state=:state`
	result, err := tx.NamedExecContext(ctx, query3, Task{ID: phase.TaskID, ActivePhase: phase.ID, State: taskState})
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
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	query := `update phases set status=:status,error=nullif(:error,''),output_snapshot=nullif(:output_snapshot,''),ended_at=:ended_at where id=:id and status='running'`
	result, err := tx.NamedExecContext(ctx, query, Phase{ID: phaseID, Status: status, Error: message, OutputSnapshot: outputSnapshot, EndedAt: now()})
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
	phase.StartedAt = now()
	query := `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(:id,:task_id,:sequence,:name,:kind,:owner,:description,:status,:attempt,:retries,:started_at,nullif(:branch_id,''),nullif(:definition_id,''),nullif(:input_snapshot,''),nullif(:output_snapshot,''),:superseded,nullif(:native_base_entry_id,''),:fork_native)`
	_, err := r.db.NamedExecContext(ctx, query, phase)
	return wrap("start phase", err)
}

func (r *PhaseRepository) End(ctx context.Context, id, status, message string) error {
	query := `update phases set status=:status,error=nullif(:error,''),ended_at=:ended_at where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Phase{ID: id, Status: status, Error: message, EndedAt: now()})
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
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	for _, check := range checks {
		query := `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,output_path,duration_ms,started_at,ended_at) values(:id,:task_id,nullif(:phase_id,''),nullif(:stage_id,''),:check_phase,nullif(:comparison_baseline,''),:name,:command,:attempt,:status,:exit_code,:output,:output_path,:duration_ms,:started_at,:ended_at)`
		if _, err = tx.NamedExecContext(ctx, query, check); err != nil {
			return wrap("publish verification check", err)
		}
	}
	for _, comparison := range comparisons {
		overlay, marshalErr := json.Marshal(comparison.OverlayPaths)
		if marshalErr != nil {
			return wrap("encode verification comparison",
				marshalErr)
		}
		query2 := `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(:id,:task_id,:phase_id,:attempt,:status,:reason,nullif(:baseline_snapshot,''),:overlay_paths_json,:created_at,:duration_ms)`
		if _, err = tx.NamedExecContext(ctx, query2, comparisonRecord{Comparison: comparison, OverlayPathsJSON: string(overlay)}); err != nil {
			return wrap("publish verification comparison",
				err,
			)
		}
	}
	ended := now()
	query3 := `update phases set status=:status,error=nullif(:error,''),output_snapshot=nullif(:output_snapshot,''),ended_at=:ended_at where id=:id and task_id=:task_id and status='running'`
	result, err := tx.NamedExecContext(ctx, query3, Phase{ID: phaseID, TaskID: taskID, Status: status, Error: message, OutputSnapshot: outputSnapshot, EndedAt: ended})
	if err != nil {
		return wrap("complete phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	query4 := `update tasks set previous_state=state,state=:to_state,active_phase=nullif(:phase_id,''),error=nullif(:error,''),ended_at=:ended_at where id=:task_id and state=:from_state`
	result, err = tx.NamedExecContext(ctx, query4, map[string]any{"task_id": taskID, "phase_id": phaseID, "from_state": from, "to_state": to, "error": message, "ended_at": nullIfTerminalState(to, ended)})
	if err != nil {
		return wrap("advance task after phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if len(approval) > 0 && approval[0] != "" {
		query5 := `update tasks set plan_digest=:plan_digest where id=:id`
		if _, err = tx.NamedExecContext(ctx, query5, Task{ID: taskID, PlanDigest: approval[0]}); err != nil {
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
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin queued phase start",
			err)
	}
	defer tx.Rollback()
	query := `update phases set status='running',started_at=:started_at,ended_at=null,error=null where task_id=:task_id and id=:id and status='queued'`
	result, err := tx.NamedExecContext(ctx, query, Phase{ID: phaseID, TaskID: taskID, StartedAt: now()})
	if err != nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	query2 := `update tasks set active_phase=:active_phase where id=:id`
	result, err = tx.NamedExecContext(ctx, query2, Task{ID: taskID, ActivePhase: phaseID})
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
	query := `update phases set native_base_entry_id=:native_base_entry_id where task_id=:task_id and id=:id and coalesce(native_base_entry_id,'')=''`
	_, err := r.db.NamedExecContext(ctx, query, Phase{ID: phaseID, TaskID: taskID, NativeBaseEntryID: entryID})
	return wrap("set phase native base",
		err)
}

// SetOutputSnapshot records the workspace snapshot a phase produced.
func (r *PhaseRepository) SetOutputSnapshot(ctx context.Context, phaseID, snapshot string) error {
	query := `update phases set output_snapshot=:output_snapshot where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Phase{ID: phaseID, OutputSnapshot: snapshot})
	return wrap("save phase output snapshot", err)
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

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at,omitempty"`
}

// StartPhaseWithEvent creates a running phase, updates its Task's active phase
// and branch head, and records the phase-start event atomically.
func (db *DB) StartPhaseWithEvent(ctx context.Context, taskDir string, phase Phase, taskState string, event Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase start", err)
	}
	defer tx.Rollback()
	started := phase.StartedAt
	if started == "" {
		started = now()
	}
	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, "running", phase.Attempt, phase.Retries, started, nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded)); err != nil {
		return wrap("insert phase", err)
	}
	if phase.BranchID != "" {
		if _, err = tx.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`, phase.ID, now(), phase.TaskID, phase.BranchID); err != nil {
			return wrap("update phase branch head", err)
		}
	}
	result, err := tx.ExecContext(ctx, `update tasks set active_phase=? where id=? and state=?`, phase.ID, phase.TaskID, taskState)
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
func (db *DB) EndPhaseWithEvent(ctx context.Context, taskDir string, phaseID, status, message, outputSnapshot string, event Event) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and status='running'`, status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), now(), phaseID)
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
		slog.Error("write derived event trace", "task_id", event.TaskID, "event_id", event.ID, "error", err)
	}
	return nil
}

func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_, err := db.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded))
	return wrap("start phase", err)
}

func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

// CompletePhaseWithTransitionAndEvent publishes a phase result, its Task
// transition, and the corresponding lifecycle event atomically. The event
// trace is derived output and is written only after the database commit.
func (db *DB) CompletePhaseWithTransitionAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message string, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message, nil, nil, nil, event)
}

// CompletePhaseWithArtifactAndTransitionAndEvent publishes a phase result, its
// required report artifact, Task progression, and lifecycle event atomically.
func (db *DB) CompletePhaseWithArtifactAndTransitionAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message string, artifact *Artifact, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message, nil, nil, artifact, event)
}

// CompletePlannerPhaseWithArtifactAndApproval publishes a planner report and
// approval candidate together with the phase transition and event.
func (db *DB) CompletePlannerPhaseWithArtifactAndApproval(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message, approval string, artifact *Artifact, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message, nil, nil, artifact, event, approval)
}

// CompleteVerificationPhaseWithEvidenceAndEvent publishes verification
// evidence, the phase result, the Task transition, and the lifecycle event in
// one transaction. Individual check observations may already be durable; the
// final publication is the authoritative successful verification boundary.
func (db *DB) CompleteVerificationPhaseWithEvidenceAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message string, checks []Check, comparisons []Comparison, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message, checks, comparisons, nil, event)
}

// CompleteVerificationPhaseWithEvidenceArtifactAndEvent publishes verification
// evidence, its deterministic report, the phase result, Task progression, and
// lifecycle event in one transaction.
func (db *DB) CompleteVerificationPhaseWithEvidenceArtifactAndEvent(ctx context.Context, taskDir string, phaseID, taskID, from, to, status, message string, checks []Check, comparisons []Comparison, artifact *Artifact, event Event) error {
	return db.completePhaseWithEvidenceAndTransitionAndEvent(ctx, taskDir, phaseID, taskID, from, to, status, message, checks, comparisons, artifact, event)
}

func (db *DB) completePhaseWithEvidenceAndTransitionAndEvent(ctx context.Context, taskDir, phaseID, taskID, from, to, status, message string, checks []Check, comparisons []Comparison, artifact *Artifact, event Event, approval ...string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase completion", err)
	}
	defer tx.Rollback()
	for _, check := range checks {
		if _, err = tx.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,artifact_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, check.ID, check.TaskID, nullIfEmpty(check.PhaseID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.ComparisonBaseline), check.Name, check.Command, check.Attempt, check.Status, check.ExitCode, check.Output, check.ArtifactPath, check.DurationMS, check.StartedAt, check.EndedAt); err != nil {
			return wrap("publish verification check", err)
		}
	}
	for _, comparison := range comparisons {
		overlay, marshalErr := json.Marshal(comparison.OverlayPaths)
		if marshalErr != nil {
			return wrap("encode verification comparison", marshalErr)
		}
		if _, err = tx.ExecContext(ctx, `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?)`, comparison.ID, comparison.TaskID, comparison.PhaseID, comparison.Attempt, comparison.Status, comparison.Reason, nullIfEmpty(comparison.BaselineSnapshot), string(overlay), comparison.CreatedAt, comparison.DurationMS); err != nil {
			return wrap("publish verification comparison", err)
		}
	}
	if artifact != nil {
		if artifact.Content != "" {
			expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(artifact.Content)))
			if artifact.Digest != expected {
				return fmt.Errorf("publish artifact: content digest mismatch")
			}
		}
		if _, err = tx.ExecContext(ctx, `insert into artifacts(id,task_id,attempt_id,type,digest,path,metadata_json,content,media_type,producer,provenance_json,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, artifact.ID, artifact.TaskID, nullIfEmpty(artifact.AttemptID), artifact.Type, artifact.Digest, nullIfEmpty(artifact.Path), nullIfEmpty(artifact.Metadata), nullIfEmpty(artifact.Content), artifact.MediaType, artifact.Producer, artifact.Provenance, artifact.CreatedAt); err != nil {
			return wrap("publish artifact", err)
		}
	}
	ended := now()
	result, err := tx.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=? and task_id=? and status='running'`, status, nullIfEmpty(message), ended, phaseID, taskID)
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
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if len(approval) > 0 && approval[0] != "" {
		if _, err = tx.ExecContext(ctx, `update tasks set plan_digest=? where id=?`, approval[0], taskID); err != nil {
			return wrap("save approval candidate", err)
		}
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
		slog.Error("write derived event trace", "task_id", taskID, "event_id", event.ID, "error", err)
	}
	return nil
}

func nullIfTerminalState(state string, ended string) any {
	if state == "completed" || state == "blocked" || state == "aborted" {
		return ended
	}
	return nil
}

func (db *DB) StartQueuedPhase(ctx context.Context, taskID, phaseID string) error {
	result, err := db.ExecContext(ctx, `update phases set status='running',started_at=?,ended_at=null,error=null where task_id=? and id=? and status='queued'`, now(), taskID, phaseID)
	if err != nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) Phases(ctx context.Context, taskID string) ([]Phase, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? order by sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		var superseded int
		if err := rows.Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded); err != nil {
			return nil, err
		}
		value.Superseded = superseded != 0
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) PhaseByID(ctx context.Context, taskID, phaseID string) (Phase, error) {
	var value Phase
	var superseded int
	err := db.QueryRowContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? and id=?`, taskID, phaseID).Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return Phase{}, ErrNotFound
	}
	value.Superseded = superseded != 0
	return value, wrap("read phase", err)
}

func (db *DB) MarkSuperseded(ctx context.Context, taskID, branchID string, keepID string) error {
	_, err := db.ExecContext(ctx, `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`, taskID, branchID, keepID)
	return wrap("mark superseded", err)
}

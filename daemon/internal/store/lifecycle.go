package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"
)

// TaskTransition describes the Task mutation that belongs to a lifecycle commit.
type TaskTransition struct {
	TaskID      string
	FromState   string
	ToState     string
	ActivePhase string
	Message     string
}

// CommitApproval atomically records plan approval, advances the Task, and
// appends the approval event.
func (db *DB) CommitApproval(ctx context.Context, taskID, fromState, digest, actor string, event Event, taskDir string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin approval", err)
	}
	defer tx.Rollback()

	approvedAt := now()
	result, err := tx.ExecContext(ctx, `update tasks set previous_state=state,state='building',active_phase=null,error=null,plan_digest=?,approval_actor=?,approval_at=?,ended_at=null where id=? and state=?`, digest, actor, approvedAt, taskID, fromState)
	if err != nil {
		return wrap("save approval", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	_, line, err := insertEvent(ctx, tx, event)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit approval", err)
	}
	if err = exportEvent(taskDir, line); err != nil {
		log.Printf("event export after committed approval: %v", err)
	}
	return nil
}

// CommitPhaseStart atomically records a running phase, its Git inputs, branch
// head, active phase, and phase-start event.
func (db *DB) CommitPhaseStart(ctx context.Context, phase Phase, inputs []PhaseRepositoryInput, event Event, taskDir string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase start", err)
	}
	defer tx.Rollback()

	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded)); err != nil {
		return wrap("save phase start", err)
	}
	for _, input := range inputs {
		if _, err = tx.ExecContext(ctx, `insert into phase_repository_inputs(phase_id,repository_id,review_base_sha,head_sha,branch_name) values(?,?,?,?,?)`, phase.ID, input.RepositoryID, input.ReviewBaseSHA, input.HeadSHA, input.BranchName); err != nil {
			return wrap("save phase Git input", err)
		}
	}
	if phase.BranchID != "" {
		result, updateErr := tx.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`, phase.ID, now(), phase.TaskID, phase.BranchID)
		if updateErr != nil {
			return wrap("move phase branch head", updateErr)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `update tasks set active_phase=? where id=?`, phase.ID, phase.TaskID); err != nil {
		return wrap("save active phase", err)
	}
	_, line, err := insertEvent(ctx, tx, event)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit phase start", err)
	}
	if err = exportEvent(taskDir, line); err != nil {
		log.Printf("event export after committed phase start: %v", err)
	}
	return nil
}

// CommitPhaseLifecycle atomically publishes a phase result, its optional Task
// transition, and the corresponding lifecycle event. The JSONL trace is a
// derived export and is written only after the database transaction commits.
func (db *DB) CommitPhaseLifecycle(ctx context.Context, phase Phase, status, message, outputSnapshot string, transition *TaskTransition, event Event, taskDir string) error {
	return db.commitPhaseLifecycle(ctx, phase, status, message, outputSnapshot, transition, nil, nil, event, taskDir)
}

// CommitAgentPhaseLifecycle atomically publishes a valid agent envelope with
// phase completion, its optional Task transition, and the lifecycle event.
func (db *DB) CommitAgentPhaseLifecycle(ctx context.Context, phase Phase, status, message, outputSnapshot string, transition *TaskTransition, envelope Envelope, event Event, taskDir string) error {
	return db.CommitAgentPhaseLifecycleWithEvidence(ctx, phase, status, message, outputSnapshot, transition, envelope, nil, event, taskDir)
}

// CommitAgentPhaseLifecycleWithEvidence atomically publishes a valid agent
// envelope and test-change evidence with phase completion and its event.
func (db *DB) CommitAgentPhaseLifecycleWithEvidence(ctx context.Context, phase Phase, status, message, outputSnapshot string, transition *TaskTransition, envelope Envelope, changes []TestChange, event Event, taskDir string) error {
	return db.commitPhaseLifecycle(ctx, phase, status, message, outputSnapshot, transition, &envelope, changes, event, taskDir)
}

func (db *DB) commitPhaseLifecycle(ctx context.Context, phase Phase, status, message, outputSnapshot string, transition *TaskTransition, envelope *Envelope, changes []TestChange, event Event, taskDir string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase lifecycle", err)
	}
	defer tx.Rollback()

	ended := any(nil)
	if status == "success" || status == "failed" || status == "blocked" || status == "aborted" {
		ended = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `update phases set status=?,error=?,output_snapshot=?,ended_at=? where id=? and task_id=?`, status, nullIfEmpty(message), nullIfEmpty(outputSnapshot), ended, phase.ID, phase.TaskID)
	if err != nil {
		return wrap("save phase lifecycle", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}

	if transition != nil {
		transitionEnded := any(nil)
		if transition.ToState == "completed" || transition.ToState == "blocked" || transition.ToState == "aborted" {
			transitionEnded = time.Now().UTC().Format(time.RFC3339Nano)
		}
		result, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`, transition.ToState, nullIfEmpty(transition.ActivePhase), nullIfEmpty(transition.Message), transitionEnded, transition.TaskID, transition.FromState)
		if err != nil {
			return wrap("save task lifecycle", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
	}
	if envelope != nil {
		if _, err = tx.ExecContext(ctx, `insert into envelopes(id,task_id,phase_id,stage_id,agent_role,output_type,payload_json,valid,attempt,created_at) values(?,?,?,?,?,?,?,?,?,?)`, envelope.ID, envelope.TaskID, envelope.PhaseID, envelope.StageID, envelope.AgentRole, envelope.OutputType, envelope.Payload, envelope.Valid, envelope.Attempt, envelope.CreatedAt); err != nil {
			return wrap("save lifecycle envelope", err)
		}
		if envelope.Valid && envelope.AgentRole == "planner" {
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(envelope.Payload)))
			if _, err = tx.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`, digest, envelope.TaskID); err != nil {
				return wrap("save lifecycle plan digest", err)
			}
		}
	}
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `insert or replace into test_changes(id,task_id,phase_id,attempt,repository_id,repository_name,path,reason,change_kind,rename_from,rename_to,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, change.ID, change.TaskID, change.PhaseID, change.Attempt, change.RepositoryID, change.RepositoryName, change.Path, change.Reason, change.ChangeKind, nullIfEmpty(change.RenameFrom), nullIfEmpty(change.RenameTo), change.CreatedAt); err != nil {
			return wrap("save lifecycle test-change evidence", err)
		}
	}

	_, line, err := insertEvent(ctx, tx, event)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return wrap("commit phase lifecycle", err)
	}
	if err = exportEvent(taskDir, line); err != nil {
		// The database is authoritative. A failed trace export must not make
		// callers report the already committed lifecycle as unsuccessful.
		log.Printf("event export after committed phase lifecycle: %v", err)
	}
	return nil
}

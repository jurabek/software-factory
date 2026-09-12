package store

import (
	"context"
	"database/sql"
	"errors"
)

type RetryResult struct {
	SourceAttemptID string `json:"source_attempt_id"`
	BranchID        string `json:"branch_id"`
	AttemptID       string `json:"attempt_id"`
	CreatedAt       string `json:"created_at"`
}

func (db *DB) ApplyRetry(ctx context.Context, key string, branch Branch, phase Phase, nextState string) (RetryResult, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return RetryResult{}, false, err
	}
	defer tx.Rollback()
	var existing RetryResult
	err = tx.QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`, phase.TaskID, key).Scan(&existing.SourceAttemptID, &existing.BranchID, &existing.AttemptID, &existing.CreatedAt)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, false, wrap("read retry request", err)
	}
	if _, err = tx.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), branch.ForkAttemptID, phase.ID, branch.Status, branch.CreatedAt, branch.CreatedAt); err != nil {
		return RetryResult{}, false, wrap("create retry branch", err)
	}
	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), phase.BranchID, nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot)); err != nil {
		return RetryResult{}, false, wrap("queue retry attempt", err)
	}
	if _, err = tx.ExecContext(ctx, `update tasks set selected_branch_id=?,previous_state=state,state=?,active_phase=?,ended_at=null,error=null where id=?`, branch.ID, nextState, phase.ID, phase.TaskID); err != nil {
		return RetryResult{}, false, wrap("select retry branch", err)
	}
	createdAt := now()
	if _, err = tx.ExecContext(ctx, `insert into retry_requests(task_id,idempotency_key,source_attempt_id,branch_id,attempt_id,created_at) values(?,?,?,?,?,?)`, phase.TaskID, key, branch.ForkAttemptID, branch.ID, phase.ID, createdAt); err != nil {
		return RetryResult{}, false, wrap("save retry request", err)
	}
	if err = tx.Commit(); err != nil {
		return RetryResult{}, false, wrap("commit retry", err)
	}
	return RetryResult{SourceAttemptID: branch.ForkAttemptID, BranchID: branch.ID, AttemptID: phase.ID, CreatedAt: createdAt}, true, nil
}
func (db *DB) RetryByIdempotencyKey(ctx context.Context, taskID, key string) (RetryResult, error) {
	var value RetryResult
	err := db.QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`, taskID, key).Scan(&value.SourceAttemptID, &value.BranchID, &value.AttemptID, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, ErrNotFound
	}
	return value, wrap("read retry request", err)
}

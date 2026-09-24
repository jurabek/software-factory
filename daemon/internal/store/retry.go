package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

type RetryResult struct {
	SourceAttemptID string `db:"source_attempt_id" json:"source_attempt_id"`
	BranchID        string `db:"branch_id" json:"branch_id"`
	AttemptID       string `db:"attempt_id" json:"attempt_id"`
	CreatedAt       string `db:"created_at" json:"created_at"`
}

type RetryRepository struct{ db *sqlx.DB }

func (r *RetryRepository) Apply(ctx context.Context, key string, branch Branch, phase Phase, nextState string) (RetryResult, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return RetryResult{}, false, err
	}
	defer tx.Rollback()
	var existing RetryResult
	query := `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`
	err = tx.QueryRowContext(ctx, query, phase.TaskID, key).Scan(&existing.SourceAttemptID, &existing.BranchID, &existing.AttemptID, &existing.CreatedAt)
	if err == nil {
		return existing, false,
			nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, false, wrap("read retry request", err)
	}
	branch.HeadAttemptID = phase.ID
	branch.UpdatedAt = branch.CreatedAt
	query2 := `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(:id,:task_id,nullif(:parent_branch_id,''),:fork_attempt_id,:head_attempt_id,:status,:created_at,:updated_at)`
	if _, err = tx.NamedExecContext(ctx, query2, branch); err != nil {
		return RetryResult{}, false, wrap("create retry branch",
			err)
	}
	phase.StartedAt = now()
	query3 := `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded,native_base_entry_id,fork_native) values(:id,:task_id,:sequence,:name,:kind,:owner,:description,:status,:attempt,:retries,:started_at,:branch_id,nullif(:definition_id,''),nullif(:input_snapshot,''),nullif(:output_snapshot,''),0,nullif(:native_base_entry_id,''),:fork_native)`
	if _, err = tx.NamedExecContext(ctx, query3, phase); err != nil {
		return RetryResult{},
			false,
			wrap("queue retry attempt",
				err)
	}
	query4 := `update tasks set selected_branch_id=:selected_branch_id,previous_state=state,state=:state,active_phase=:active_phase,ended_at=null,error=null where id=:id`
	if _, err = tx.NamedExecContext(ctx, query4, Task{ID: phase.TaskID, SelectedBranchID: branch.ID, State: nextState, ActivePhase: phase.ID}); err != nil {
		return RetryResult{}, false, wrap("select retry branch", err)
	}
	createdAt := now()
	query5 := `insert into retry_requests(task_id,idempotency_key,source_attempt_id,branch_id,attempt_id,created_at) values(:task_id,:idempotency_key,:source_attempt_id,:branch_id,:attempt_id,:created_at)`
	if _, err = tx.NamedExecContext(ctx, query5, map[string]any{"task_id": phase.TaskID, "idempotency_key": key, "source_attempt_id": branch.ForkAttemptID, "branch_id": branch.ID, "attempt_id": phase.ID, "created_at": createdAt}); err != nil {
		return RetryResult{}, false, wrap("save retry request", err)
	}
	if err = tx.Commit(); err != nil {
		return RetryResult{}, false, wrap("commit retry", err)
	}
	return RetryResult{
		SourceAttemptID: branch.ForkAttemptID,
		BranchID:        branch.ID, AttemptID: phase.ID,
		CreatedAt: createdAt,
	}, true, nil
}

func (r *RetryRepository) ByIdempotencyKey(ctx context.Context, taskID, key string) (RetryResult, error) {
	var value RetryResult
	query := `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`
	err := r.db.QueryRowContext(ctx, query, taskID,
		key).Scan(&value.SourceAttemptID, &value.BranchID, &value.AttemptID, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, ErrNotFound
	}
	return value, wrap("read retry request", err)
}

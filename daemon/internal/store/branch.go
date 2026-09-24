package store

import (
	"context"
	"database/sql"
	"errors"
)

type Branch struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	ParentBranchID string `json:"parent_branch_id,omitempty"`
	ForkAttemptID  string `json:"fork_attempt_id,omitempty"`
	HeadAttemptID  string `json:"head_attempt_id,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type BranchRepository struct{ db *sql.DB }

func (r *BranchRepository) Create(ctx context.Context, branch Branch) error {
	query := `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, query, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), nullIfEmpty(branch.ForkAttemptID), nullIfEmpty(branch.HeadAttemptID), branch.Status, branch.CreatedAt, branch.CreatedAt)
	return wrap("create branch",
		err)
}

func (r *BranchRepository) List(ctx context.Context, taskID string) ([]Branch, error) {
	query := `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? order by created_at`
	rows, err := r.db.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Branch,
		0)
	for rows.Next() {
		var value Branch
		if err = rows.Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *BranchRepository) Get(ctx context.Context, taskID, branchID string) (Branch, error) {
	var value Branch
	query := `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? and id=?`
	err := r.db.QueryRowContext(ctx, query, taskID, branchID).Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, ErrNotFound
	}
	return value, wrap("read branch",
		err)
}

func (r *BranchRepository) SetHead(ctx context.Context, taskID, branchID, headAttemptID string) error {
	query := `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`
	_, err := r.db.ExecContext(ctx, query, nullIfEmpty(headAttemptID), now(), taskID, branchID)
	return wrap("move branch head",
		err)
}

func (r *BranchRepository) Select(ctx context.Context, taskID, branchID string) error {
	query := `update tasks set selected_branch_id=? where id=?`
	_, err := r.db.ExecContext(ctx, query, nullIfEmpty(branchID), taskID)
	return wrap("select branch",
		err)
}

func (r *BranchRepository) TaskHeadAttempt(ctx context.Context, taskID string) string {
	var selected string
	query := `select coalesce(selected_branch_id,'') from tasks where id=?`
	_ = r.db.QueryRowContext(ctx, query, taskID).Scan(&selected)
	if selected == "" {
		return ""
	}
	var head string
	query2 := `select coalesce(head_attempt_id,'') from branches where task_id=? and id=?`
	_ = r.db.QueryRowContext(ctx, query2,
		taskID, selected).Scan(&head)
	return head
}

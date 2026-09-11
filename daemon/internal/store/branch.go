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

func (db *DB) CreateBranch(ctx context.Context, branch Branch) error {
	_, err := db.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), nullIfEmpty(branch.ForkAttemptID), nullIfEmpty(branch.HeadAttemptID), branch.Status, branch.CreatedAt, branch.CreatedAt)
	return wrap("create branch", err)
}
func (db *DB) Branches(ctx context.Context, taskID string) ([]Branch, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Branch, 0)
	for rows.Next() {
		var value Branch
		if err = rows.Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) Branch(ctx context.Context, taskID, branchID string) (Branch, error) {
	var value Branch
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? and id=?`, taskID, branchID).Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, ErrNotFound
	}
	return value, wrap("read branch", err)
}
func (db *DB) SetBranchHead(ctx context.Context, taskID, branchID, headAttemptID string) error {
	_, err := db.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`, nullIfEmpty(headAttemptID), now(), taskID, branchID)
	return wrap("move branch head", err)
}
func (db *DB) SelectBranch(ctx context.Context, taskID, branchID string) error {
	_, err := db.ExecContext(ctx, `update tasks set selected_branch_id=? where id=?`, nullIfEmpty(branchID), taskID)
	return wrap("select branch", err)
}
func (db *DB) TaskHeadAttempt(ctx context.Context, taskID string) string {
	var selected string
	_ = db.QueryRowContext(ctx, `select coalesce(selected_branch_id,'') from tasks where id=?`, taskID).Scan(&selected)
	if selected == "" {
		return ""
	}
	var head string
	_ = db.QueryRowContext(ctx, `select coalesce(head_attempt_id,'') from branches where task_id=? and id=?`, taskID, selected).Scan(&head)
	return head
}

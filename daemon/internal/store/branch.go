package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

type Branch struct {
	ID             string `db:"id" json:"id"`
	TaskID         string `db:"task_id" json:"task_id"`
	ParentBranchID string `db:"parent_branch_id" json:"parent_branch_id,omitempty"`
	ForkAttemptID  string `db:"fork_attempt_id" json:"fork_attempt_id,omitempty"`
	HeadAttemptID  string `db:"head_attempt_id" json:"head_attempt_id,omitempty"`
	Status         string `db:"status" json:"status"`
	CreatedAt      string `db:"created_at" json:"created_at"`
	UpdatedAt      string `db:"updated_at" json:"updated_at"`
}

type BranchRepository struct{ db *sqlx.DB }

func (r *BranchRepository) Create(ctx context.Context, branch Branch) error {
	branch.UpdatedAt = branch.CreatedAt
	query := `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(:id,:task_id,nullif(:parent_branch_id,''),nullif(:fork_attempt_id,''),nullif(:head_attempt_id,''),:status,:created_at,:updated_at)`
	_, err := r.db.NamedExecContext(ctx, query, branch)
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
	query := `update branches set head_attempt_id=nullif(:head_attempt_id,''),updated_at=:updated_at where task_id=:task_id and id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Branch{ID: branchID, TaskID: taskID, HeadAttemptID: headAttemptID, UpdatedAt: now()})
	return wrap("move branch head",
		err)
}

func (r *BranchRepository) Select(ctx context.Context, taskID, branchID string) error {
	query := `update tasks set selected_branch_id=nullif(:selected_branch_id,'') where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Task{ID: taskID, SelectedBranchID: branchID})
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

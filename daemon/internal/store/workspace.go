package store

import (
	"context"
	"database/sql"
	"errors"
)

type WorkspaceOperation struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	RepositoryID string `json:"repository_id,omitempty"`
	AttemptID    string `json:"attempt_id,omitempty"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	RequestJSON  string `json:"request_json"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (db *DB) CreateWorkspaceOperation(ctx context.Context, operation WorkspaceOperation) error {
	_, err := db.ExecContext(ctx, `insert into workspace_operations(id,task_id,repository_id,attempt_id,kind,status,request_json,error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, operation.ID, operation.TaskID, nullIfEmpty(operation.RepositoryID), nullIfEmpty(operation.AttemptID), operation.Kind, operation.Status, operation.RequestJSON, nullIfEmpty(operation.Error), operation.CreatedAt, operation.UpdatedAt)
	return wrap("create workspace operation", err)
}

func (db *DB) UpdateWorkspaceOperation(ctx context.Context, id, status, operationError string) error {
	_, err := db.ExecContext(ctx, `update workspace_operations set status=?,error=?,updated_at=? where id=?`, status, nullIfEmpty(operationError), now(), id)
	return wrap("update workspace operation", err)
}

func (db *DB) WorkspaceOperation(ctx context.Context, id string) (WorkspaceOperation, error) {
	value := WorkspaceOperation{}
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(repository_id,''),coalesce(attempt_id,''),kind,status,request_json,coalesce(error,''),created_at,updated_at from workspace_operations where id=?`, id).Scan(&value.ID, &value.TaskID, &value.RepositoryID, &value.AttemptID, &value.Kind, &value.Status, &value.RequestJSON, &value.Error, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceOperation{}, ErrNotFound
	}
	return value, wrap("read workspace operation", err)
}

func (db *DB) WorkspaceOperations(ctx context.Context, taskID string) ([]WorkspaceOperation, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(repository_id,''),coalesce(attempt_id,''),kind,status,request_json,coalesce(error,''),created_at,updated_at from workspace_operations where task_id=? order by created_at,id`, taskID)
	if err != nil {
		return nil, wrap("read workspace operations", err)
	}
	defer rows.Close()
	values := make([]WorkspaceOperation, 0)
	for rows.Next() {
		var value WorkspaceOperation
		if err = rows.Scan(&value.ID, &value.TaskID, &value.RepositoryID, &value.AttemptID, &value.Kind, &value.Status, &value.RequestJSON, &value.Error, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, wrap("scan workspace operation", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) RecoverWorkspaceOperations(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `update workspace_operations set status='interrupted',error='server restarted during workspace operation',updated_at=? where status in ('planned','running')`, now())
	return wrap("recover workspace operations", err)
}

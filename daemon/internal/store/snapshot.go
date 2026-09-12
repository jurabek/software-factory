package store

import (
	"context"
	"database/sql"
	"errors"
)

type WorkspaceSnapshot struct {
	Digest    string `json:"digest"`
	TaskID    string `json:"task_id"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Manifest  string `json:"manifest_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (db *DB) SaveSnapshot(ctx context.Context, snapshot WorkspaceSnapshot) error {
	manifest := snapshot.Manifest
	if manifest == "" {
		manifest = "{}"
	}
	_, err := db.ExecContext(ctx, `insert or ignore into workspace_snapshots(digest,task_id,path,size_bytes,manifest_json,created_at) values(?,?,?,?,?,?)`, snapshot.Digest, snapshot.TaskID, snapshot.Path, snapshot.SizeBytes, manifest, snapshot.CreatedAt)
	return wrap("save snapshot", err)
}
func (db *DB) Snapshot(ctx context.Context, digest string) (WorkspaceSnapshot, error) {
	var value WorkspaceSnapshot
	err := db.QueryRowContext(ctx, `select digest,task_id,coalesce(path,''),coalesce(size_bytes,0),coalesce(manifest_json,'{}'),created_at from workspace_snapshots where digest=?`, digest).Scan(&value.Digest, &value.TaskID, &value.Path, &value.SizeBytes, &value.Manifest, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceSnapshot{}, ErrNotFound
	}
	return value, wrap("read snapshot", err)
}

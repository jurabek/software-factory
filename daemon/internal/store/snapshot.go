package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

type WorkspaceSnapshot struct {
	Digest    string `db:"digest" json:"digest"`
	TaskID    string `db:"task_id" json:"task_id"`
	Path      string `db:"path" json:"path"`
	SizeBytes int64  `db:"size_bytes" json:"size_bytes"`
	Manifest  string `db:"manifest_json" json:"manifest_json,omitempty"`
	CreatedAt string `db:"created_at" json:"created_at"`
}

type SnapshotRepository struct{ db *sqlx.DB }

func (r *SnapshotRepository) Save(ctx context.Context, snapshot WorkspaceSnapshot) error {
	manifest := snapshot.Manifest
	if manifest == "" {
		manifest = "{}"
	}
	snapshot.Manifest = manifest
	query := `insert or ignore into workspace_snapshots(digest,task_id,path,size_bytes,manifest_json,created_at) values(:digest,:task_id,:path,:size_bytes,:manifest_json,:created_at)`
	_, err := r.db.NamedExecContext(ctx, query, snapshot)
	return wrap("save snapshot", err)
}

func (r *SnapshotRepository) Get(ctx context.Context, digest string) (WorkspaceSnapshot, error) {
	var value WorkspaceSnapshot
	query := `select digest,task_id,coalesce(path,''),coalesce(size_bytes,0),coalesce(manifest_json,'{}'),created_at from workspace_snapshots where digest=?`
	err := r.db.QueryRowContext(ctx, query, digest).Scan(&value.Digest, &value.TaskID, &value.Path, &value.SizeBytes, &value.Manifest, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceSnapshot{}, ErrNotFound
	}
	return value, wrap("read snapshot",
		err)
}

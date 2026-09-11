package store

import (
	"context"
	"database/sql"
	"errors"
)

type Artifact struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	Type      string `json:"type"`
	Digest    string `json:"digest"`
	Path      string `json:"path"`
	Metadata  string `json:"metadata_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (db *DB) CreateArtifact(ctx context.Context, artifact Artifact) error {
	_, err := db.ExecContext(ctx, `insert into artifacts(id,task_id,attempt_id,type,digest,path,metadata_json,created_at) values(?,?,?,?,?,?,?,?)`, artifact.ID, artifact.TaskID, nullIfEmpty(artifact.AttemptID), artifact.Type, artifact.Digest, artifact.Path, nullIfEmpty(artifact.Metadata), artifact.CreatedAt)
	return wrap("create artifact", err)
}
func (db *DB) Artifacts(ctx context.Context, taskID string) ([]Artifact, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(attempt_id,''),type,coalesce(digest,''),coalesce(path,''),coalesce(metadata_json,'{}'),created_at from artifacts where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Artifact, 0)
	for rows.Next() {
		var value Artifact
		if err = rows.Scan(&value.ID, &value.TaskID, &value.AttemptID, &value.Type, &value.Digest, &value.Path, &value.Metadata, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) Artifact(ctx context.Context, taskID, artifactID string) (Artifact, error) {
	var value Artifact
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(attempt_id,''),type,coalesce(digest,''),coalesce(path,''),coalesce(metadata_json,'{}'),created_at from artifacts where task_id=? and id=?`, taskID, artifactID).Scan(&value.ID, &value.TaskID, &value.AttemptID, &value.Type, &value.Digest, &value.Path, &value.Metadata, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	return value, wrap("read artifact", err)
}

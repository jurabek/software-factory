package store

import (
	"context"
	"database/sql"
	"errors"
)

type PhaseDefinition struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	PhaseKey       string `json:"phase_key"`
	Revision       int    `json:"revision"`
	Executor       string `json:"executor"`
	Owner          string `json:"owner"`
	Spec           string `json:"spec_json"`
	Digest         string `json:"digest"`
	ParentRevision int    `json:"parent_revision"`
	CreatedAt      string `json:"created_at"`
}

func (db *DB) CreateDefinition(ctx context.Context, definition PhaseDefinition) error {
	_, err := db.ExecContext(ctx, `insert into phase_definitions(id,task_id,phase_key,revision,executor,owner,spec_json,digest,parent_revision,created_at) values(?,?,?,?,?,?,?,?,?,?)`, definition.ID, definition.TaskID, definition.PhaseKey, definition.Revision, definition.Executor, definition.Owner, definition.Spec, definition.Digest, definition.ParentRevision, definition.CreatedAt)
	return wrap("create phase definition", err)
}
func (db *DB) LatestDefinition(ctx context.Context, taskID, phaseKey string) (PhaseDefinition, error) {
	var value PhaseDefinition
	err := db.QueryRowContext(ctx, `select id,task_id,phase_key,revision,coalesce(executor,''),coalesce(owner,''),coalesce(spec_json,'{}'),coalesce(digest,''),coalesce(parent_revision,0),created_at from phase_definitions where task_id=? and phase_key=? order by revision desc limit 1`, taskID, phaseKey).Scan(&value.ID, &value.TaskID, &value.PhaseKey, &value.Revision, &value.Executor, &value.Owner, &value.Spec, &value.Digest, &value.ParentRevision, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseDefinition{}, ErrNotFound
	}
	return value, wrap("read phase definition", err)
}

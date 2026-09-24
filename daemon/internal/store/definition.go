package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

type PhaseDefinition struct {
	ID             string `db:"id" json:"id"`
	TaskID         string `db:"task_id" json:"task_id"`
	PhaseKey       string `db:"phase_key" json:"phase_key"`
	Revision       int    `db:"revision" json:"revision"`
	Executor       string `db:"executor" json:"executor"`
	Owner          string `db:"owner" json:"owner"`
	Spec           string `db:"spec_json" json:"spec_json"`
	Digest         string `db:"digest" json:"digest"`
	ParentRevision int    `db:"parent_revision" json:"parent_revision"`
	CreatedAt      string `db:"created_at" json:"created_at"`
}

type DefinitionRepository struct{ db *sqlx.DB }

func (r *DefinitionRepository) Create(ctx context.Context, definition PhaseDefinition) error {
	query := `insert into phase_definitions(id,task_id,phase_key,revision,executor,owner,spec_json,digest,parent_revision,created_at) values(:id,:task_id,:phase_key,:revision,:executor,:owner,:spec_json,:digest,:parent_revision,:created_at)`
	_, err := r.db.NamedExecContext(ctx, query, definition)
	return wrap("create phase definition",
		err)
}

func (r *DefinitionRepository) Latest(ctx context.Context, taskID, phaseKey string) (PhaseDefinition, error) {
	var value PhaseDefinition
	query := `select id,task_id,phase_key,revision,coalesce(executor,''),coalesce(owner,''),coalesce(spec_json,'{}'),coalesce(digest,''),coalesce(parent_revision,0),created_at from phase_definitions where task_id=? and phase_key=? order by revision desc limit 1`
	err := r.db.QueryRowContext(ctx, query, taskID, phaseKey).Scan(&value.ID, &value.TaskID, &value.PhaseKey, &value.Revision, &value.Executor, &value.Owner, &value.Spec, &value.Digest, &value.ParentRevision, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseDefinition{}, ErrNotFound
	}
	return value, wrap("read phase definition", err)
}

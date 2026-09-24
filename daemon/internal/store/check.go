package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type Check struct {
	ID                 string `db:"id" json:"id"`
	TaskID             string `db:"task_id" json:"task_id"`
	PhaseID            string `db:"phase_id" json:"phase_id"`
	StageID            string `db:"stage_id" json:"stage_id"`
	Phase              string `db:"check_phase" json:"phase"`
	ComparisonBaseline string `db:"comparison_baseline" json:"comparison_baseline,omitempty"`
	Name               string `db:"name" json:"name"`
	Command            string `db:"command" json:"command"`
	Status             string `db:"status" json:"status"`
	Output             string `db:"output" json:"output"`
	OutputPath         string `db:"output_path" json:"output_path"`
	Attempt            int    `db:"attempt" json:"attempt"`
	ExitCode           int    `db:"exit_code" json:"exit_code"`
	DurationMS         int    `db:"duration_ms" json:"duration_ms"`
	StartedAt          string `db:"started_at" json:"started_at"`
	EndedAt            string `db:"ended_at" json:"ended_at"`
}

type CheckRepository struct{ db *sqlx.DB }

func (r *CheckRepository) Save(ctx context.Context, check Check) error {
	query := `insert or replace into checks(id,task_id,phase_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,output_path,duration_ms,started_at,ended_at) values(:id,:task_id,nullif(:phase_id,''),nullif(:stage_id,''),:check_phase,nullif(:comparison_baseline,''),:name,:command,:attempt,:status,:exit_code,:output,:output_path,:duration_ms,:started_at,:ended_at)`
	_, err := r.db.NamedExecContext(ctx, query, check)
	return wrap("save check",
		err)
}

func (r *CheckRepository) List(ctx context.Context, taskID string) ([]Check, error) {
	query := `select id,task_id,coalesce(phase_id,''),coalesce(stage_id,''),coalesce(check_phase,'primary'),coalesce(comparison_baseline,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(output_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where task_id=? order by rowid`
	rows, err := r.db.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.StageID, &value.Phase, &value.ComparisonBaseline, &value.Name, &value.Command, &value.Attempt, &value.Status, &value.ExitCode, &value.Output, &value.OutputPath, &value.DurationMS, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

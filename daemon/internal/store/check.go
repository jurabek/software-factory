package store

import "context"

type Check struct {
	ID                 string `json:"id"`
	TaskID             string `json:"task_id"`
	PhaseID            string `json:"phase_id"`
	RepositoryID       string `json:"repository_id"`
	StageID            string `json:"stage_id"`
	Phase              string `json:"phase"`
	ComparisonBaseline string `json:"comparison_baseline,omitempty"`
	Name               string `json:"name"`
	Command            string `json:"command"`
	Status             string `json:"status"`
	Output             string `json:"output"`
	ArtifactPath       string `json:"artifact_path"`
	Attempt            int    `json:"attempt"`
	ExitCode           int    `json:"exit_code"`
	DurationMS         int    `json:"duration_ms"`
	StartedAt          string `json:"started_at"`
	EndedAt            string `json:"ended_at"`
}

func (db *DB) SaveCheck(ctx context.Context, check Check) error {
	_, err := db.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,repository_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,artifact_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, check.ID, check.TaskID, nullIfEmpty(check.PhaseID), nullIfEmpty(check.RepositoryID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.ComparisonBaseline), check.Name, check.Command, check.Attempt, check.Status, check.ExitCode, check.Output, check.ArtifactPath, check.DurationMS, check.StartedAt, check.EndedAt)
	return wrap("save check", err)
}
func (db *DB) Checks(ctx context.Context, taskID string) ([]Check, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),coalesce(repository_id,''),coalesce(stage_id,''),coalesce(check_phase,'primary'),coalesce(comparison_baseline,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(artifact_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where task_id=? order by rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.RepositoryID, &value.StageID, &value.Phase, &value.ComparisonBaseline, &value.Name, &value.Command, &value.Attempt, &value.Status, &value.ExitCode, &value.Output, &value.ArtifactPath, &value.DurationMS, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

package store

import (
	"context"
	"database/sql"
	"errors"
)

type Phase struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	StageID        string `json:"stage_id,omitempty"`
	Kind           string `json:"kind"`
	Owner          string `json:"owner"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Sequence       int    `json:"sequence"`
	Attempt        int    `json:"attempt"`
	Retries        int    `json:"retries"`
	BranchID       string `json:"branch_id,omitempty"`
	DefinitionID   string `json:"definition_id,omitempty"`
	DefinitionRev  int    `json:"definition_revision,omitempty"`
	InputSnapshot  string `json:"input_snapshot,omitempty"`
	OutputSnapshot string `json:"output_snapshot,omitempty"`
	Superseded     bool   `json:"superseded,omitempty"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at,omitempty"`
}

func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_, err := db.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded))
	return wrap("start phase", err)
}

func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

func (db *DB) StartQueuedPhase(ctx context.Context, taskID, phaseID string) error {
	result, err := db.ExecContext(ctx, `update phases set status='running',started_at=?,ended_at=null,error=null where task_id=? and id=? and status='queued'`, now(), taskID, phaseID)
	if err != nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) Phases(ctx context.Context, taskID string) ([]Phase, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? order by sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		var superseded int
		if err := rows.Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded); err != nil {
			return nil, err
		}
		value.Superseded = superseded != 0
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) PhaseByID(ctx context.Context, taskID, phaseID string) (Phase, error) {
	var value Phase
	var superseded int
	err := db.QueryRowContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? and id=?`, taskID, phaseID).Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return Phase{}, ErrNotFound
	}
	value.Superseded = superseded != 0
	return value, wrap("read phase", err)
}

func (db *DB) MarkSuperseded(ctx context.Context, taskID, branchID string, keepID string) error {
	_, err := db.ExecContext(ctx, `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`, taskID, branchID, keepID)
	return wrap("mark superseded", err)
}

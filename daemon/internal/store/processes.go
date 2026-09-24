package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

type process struct {
	TaskID    string `db:"task_id"`
	PhaseID   string `db:"phase_id"`
	Kind      string `db:"kind"`
	Name      string `db:"name"`
	PID       int    `db:"pid"`
	Command   string `db:"display_command"`
	Status    string `db:"status"`
	ExitCode  int    `db:"exit_code"`
	StartedAt string `db:"started_at"`
	EndedAt   string `db:"ended_at"`
}

type ProcessRepository struct{ db *sqlx.DB }

func (r *ProcessRepository) Start(ctx context.Context, taskID, phaseID, kind, name string, pid int, command string) (int64, error) {
	query := `insert into processes(task_id,phase_id,kind,name,pid,display_command,status,started_at) values(:task_id,nullif(:phase_id,''),:kind,:name,:pid,:display_command,:status,:started_at)`
	result, err := r.db.NamedExecContext(ctx, query, process{TaskID: taskID, PhaseID: phaseID, Kind: kind, Name: name, PID: pid, Command: command, Status: "running", StartedAt: now()})
	if err != nil {
		return 0, fmt.Errorf("start process: %w",
			err)
	}
	return result.LastInsertId()
}

func (r *ProcessRepository) End(ctx context.Context, taskID string, pid, exitCode int) error {
	query := `update processes set status=case when :exit_code=0 then 'ended' else 'failed' end,exit_code=:exit_code,ended_at=:ended_at where task_id=:task_id and pid=:pid and status='running'`
	_, err := r.db.NamedExecContext(ctx, query, process{TaskID: taskID, PID: pid, ExitCode: exitCode, EndedAt: now()})
	return wrap("end process", err)
}

func (r *ProcessRepository) Recover(ctx context.Context) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ended := now()
	params := map[string]any{"ended_at": ended}
	query := `update processes set status='failed',ended_at=:ended_at where status='running'`
	if _, err = tx.NamedExecContext(ctx, query, params); err != nil {
		return err
	}
	query2 := `update phases set status='interrupted',error='server restarted during active phase',ended_at=:ended_at where status in ('running','queued')`
	if _, err = tx.NamedExecContext(ctx, query2, params); err != nil {
		return err
	}
	query3 := `update tasks set previous_state=state,state='blocked',error='server restarted during active phase',ended_at=:ended_at where state in ('preparing','planning','building','checking','reviewing')`
	if _, err = tx.NamedExecContext(ctx, query3, params); err != nil {
		return err
	}
	return tx.Commit()
}

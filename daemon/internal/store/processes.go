package store

import (
	"context"
	"database/sql"
	"fmt"
)

type ProcessRepository struct{ db *sql.DB }

func (r *ProcessRepository) Start(ctx context.Context, taskID, phaseID, kind, name string, pid int, command string) (int64, error) {
	query := `insert into processes(task_id,phase_id,kind,name,pid,display_command,status,started_at) values(?,?,?,?,?,?,?,?)`
	result, err := r.db.ExecContext(ctx, query, taskID, nullIfEmpty(phaseID), kind, name, pid, command, "running",
		now())
	if err != nil {
		return 0, fmt.Errorf("start process: %w",
			err)
	}
	return result.LastInsertId()
}

func (r *ProcessRepository) End(ctx context.Context, taskID string, pid, exitCode int) error {
	query := `update processes set status=case when ?=0 then 'ended' else 'failed' end,exit_code=?,ended_at=? where task_id=? and pid=? and status='running'`
	_, err := r.db.ExecContext(ctx, query, exitCode, exitCode,
		now(), taskID, pid)
	return wrap("end process", err)
}

func (r *ProcessRepository) Recover(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ended := now()
	query := `update processes set status='failed',ended_at=? where status='running'`
	if _, err = tx.ExecContext(ctx, query,
		ended); err != nil {
		return err
	}
	query2 := `update phases set status='interrupted',error='server restarted during active phase',ended_at=? where status in ('running','queued')`
	if _, err = tx.ExecContext(ctx, query2, ended); err != nil {
		return err
	}
	query3 := `update branches set status='blocked',updated_at=? where task_id in (select id from tasks where state in ('preparing','planning','building','checking','reviewing')) and status='active'`
	if _, err = tx.ExecContext(ctx, query3, ended); err != nil {
		return err
	}
	query4 := `update tasks set previous_state=state,state='blocked',error='server restarted during active phase',ended_at=? where state in ('preparing','planning','building','checking','reviewing')`
	if _, err = tx.ExecContext(ctx, query4, ended); err != nil {
		return err
	}
	return tx.Commit()
}

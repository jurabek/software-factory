package store

import (
	"context"
	"fmt"
)

func (db *DB) StartProcess(ctx context.Context, taskID, phaseID, kind, name string, pid int, command string) (int64, error) {
	result, err := db.ExecContext(ctx, `insert into processes(task_id,phase_id,kind,name,pid,display_command,status,started_at) values(?,?,?,?,?,?,?,?)`, taskID, nullIfEmpty(phaseID), kind, name, pid, command, "running", now())
	if err != nil {
		return 0, fmt.Errorf("start process: %w", err)
	}
	return result.LastInsertId()
}
func (db *DB) EndProcess(ctx context.Context, taskID string, pid, exitCode int) error {
	_, err := db.ExecContext(ctx, `update processes set status=case when ?=0 then 'ended' else 'failed' end,exit_code=?,ended_at=? where task_id=? and pid=? and status='running'`, exitCode, exitCode, now(), taskID, pid)
	return wrap("end process", err)
}

package store

import "context"

func (db *DB) Recover(ctx context.Context) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ended := now()
	if _, err = tx.ExecContext(ctx, `update processes set status='failed',ended_at=? where status='running'`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update phases set status='interrupted',error='server restarted during active phase',ended_at=? where status in ('running','queued')`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update branches set status='blocked',updated_at=? where task_id in (select id from tasks where state in ('preparing','planning','building','checking','reviewing')) and status='active'`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state='blocked',error='server restarted during active phase: retry, revise, or repair explicitly',ended_at=? where state in ('preparing','planning','building','checking','reviewing')`, ended); err != nil {
		return err
	}
	return tx.Commit()
}

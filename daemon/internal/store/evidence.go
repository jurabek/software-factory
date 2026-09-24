package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type TestChange struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	PhaseID    string `json:"phase_id"`
	Attempt    int    `json:"attempt"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	ChangeKind string `json:"change_kind"`
	RenameFrom string `json:"rename_from,omitempty"`
	RenameTo   string `json:"rename_to,omitempty"`
	CreatedAt  string `json:"created_at"`
}
type Comparison struct {
	ID               string   `json:"id"`
	TaskID           string   `json:"task_id"`
	PhaseID          string   `json:"phase_id"`
	Attempt          int      `json:"attempt"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	BaselineSnapshot string   `json:"baseline_snapshot,omitempty"`
	OverlayPaths     []string `json:"overlay_paths"`
	CreatedAt        string   `json:"created_at"`
	DurationMS       int      `json:"duration_ms"`
}

type EvidenceRepository struct{ db *sql.DB }

func (r *EvidenceRepository) SaveTestChanges(ctx context.Context, changes []TestChange) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin test-change evidence",
			err)
	}
	defer tx.Rollback()
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `insert or replace into test_changes(id,task_id,phase_id,attempt,path,reason,change_kind,rename_from,rename_to,created_at) values(?,?,?,?,?,?,?,?,?,?)`, change.ID, change.TaskID,
			change.PhaseID, change.Attempt, change.Path, change.Reason,
			change.ChangeKind, nullIfEmpty(change.RenameFrom), nullIfEmpty(change.RenameTo), change.CreatedAt); err != nil {
			return wrap("save test-change evidence", err)
		}
	}
	return wrap("commit test-change evidence",
		tx.Commit())
}

func (r *EvidenceRepository) TestChanges(ctx context.Context, taskID string) ([]TestChange, error) {
	rows, err := r.db.QueryContext(ctx, `select id,task_id,phase_id,attempt,path,reason,change_kind,coalesce(rename_from,''),coalesce(rename_to,''),created_at from test_changes where task_id=? order by created_at,rowid`, taskID)
	if err != nil {
		return nil, wrap("read test-change evidence", err)
	}
	defer rows.Close()
	values := make([]TestChange, 0)
	for rows.Next() {
		var value TestChange
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt, &value.Path, &value.Reason, &value.ChangeKind, &value.RenameFrom, &value.RenameTo, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *EvidenceRepository) SaveComparison(ctx context.Context, value Comparison) error {
	overlay, err := json.Marshal(value.OverlayPaths)
	if err != nil {
		return wrap("encode comparison overlay paths",
			err)
	}
	_, err = r.db.ExecContext(ctx, `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.TaskID, value.PhaseID, value.Attempt,
		value.Status, value.Reason, nullIfEmpty(value.BaselineSnapshot), string(overlay), value.CreatedAt, value.DurationMS)
	return wrap("save comparison", err)
}

func (r *EvidenceRepository) Comparisons(ctx context.Context, taskID string) ([]Comparison, error) {
	rows, err := r.db.QueryContext(ctx, `select id,task_id,phase_id,attempt,status,reason,coalesce(baseline_snapshot,''),overlay_paths_json,created_at,duration_ms from comparisons where task_id=? order by created_at,rowid`, taskID)
	if err != nil {
		return nil,
			wrap("read comparisons", err)
	}
	defer rows.Close()
	values := make([]Comparison, 0)
	for rows.Next() {
		var value Comparison
		var overlay string
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt, &value.Status, &value.Reason, &value.BaselineSnapshot, &overlay, &value.CreatedAt, &value.DurationMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(overlay), &value.OverlayPaths); err != nil {
			return nil, wrap("decode comparison overlay paths",
				err)
		}
		values = append(values,
			value)
	}
	return values, rows.Err()
}

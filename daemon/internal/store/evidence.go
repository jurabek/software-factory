package store

import (
	"context"
	"encoding/json"

	"github.com/jmoiron/sqlx"
)

type TestChange struct {
	ID         string `db:"id" json:"id"`
	TaskID     string `db:"task_id" json:"task_id"`
	PhaseID    string `db:"phase_id" json:"phase_id"`
	Attempt    int    `db:"attempt" json:"attempt"`
	Path       string `db:"path" json:"path"`
	Reason     string `db:"reason" json:"reason"`
	ChangeKind string `db:"change_kind" json:"change_kind"`
	RenameFrom string `db:"rename_from" json:"rename_from,omitempty"`
	RenameTo   string `db:"rename_to" json:"rename_to,omitempty"`
	CreatedAt  string `db:"created_at" json:"created_at"`
}
type Comparison struct {
	ID               string   `db:"id" json:"id"`
	TaskID           string   `db:"task_id" json:"task_id"`
	PhaseID          string   `db:"phase_id" json:"phase_id"`
	Attempt          int      `db:"attempt" json:"attempt"`
	Status           string   `db:"status" json:"status"`
	Reason           string   `db:"reason" json:"reason"`
	BaselineSnapshot string   `db:"baseline_snapshot" json:"baseline_snapshot,omitempty"`
	OverlayPaths     []string `db:"-" json:"overlay_paths"`
	CreatedAt        string   `db:"created_at" json:"created_at"`
	DurationMS       int      `db:"duration_ms" json:"duration_ms"`
}

type comparisonRecord struct {
	Comparison
	OverlayPathsJSON string `db:"overlay_paths_json"`
}

type EvidenceRepository struct{ db *sqlx.DB }

func (r *EvidenceRepository) SaveTestChanges(ctx context.Context, changes []TestChange) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin test-change evidence",
			err)
	}
	defer tx.Rollback()
	for _, change := range changes {
		query := `insert or replace into test_changes(id,task_id,phase_id,attempt,path,reason,change_kind,rename_from,rename_to,created_at) values(:id,:task_id,:phase_id,:attempt,:path,:reason,:change_kind,nullif(:rename_from,''),nullif(:rename_to,''),:created_at)`
		if _, err = tx.NamedExecContext(ctx, query, change); err != nil {
			return wrap("save test-change evidence", err)
		}
	}
	return wrap("commit test-change evidence",
		tx.Commit())
}

func (r *EvidenceRepository) TestChanges(ctx context.Context, taskID string) ([]TestChange, error) {
	query := `select id,task_id,phase_id,attempt,path,reason,change_kind,coalesce(rename_from,''),coalesce(rename_to,''),created_at from test_changes where task_id=? order by created_at,rowid`
	rows, err := r.db.QueryContext(ctx, query, taskID)
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
	query := `insert or replace into comparisons(id,task_id,phase_id,attempt,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(:id,:task_id,:phase_id,:attempt,:status,:reason,nullif(:baseline_snapshot,''),:overlay_paths_json,:created_at,:duration_ms)`
	_, err = r.db.NamedExecContext(ctx, query, comparisonRecord{Comparison: value, OverlayPathsJSON: string(overlay)})
	return wrap("save comparison", err)
}

func (r *EvidenceRepository) Comparisons(ctx context.Context, taskID string) ([]Comparison, error) {
	query := `select id,task_id,phase_id,attempt,status,reason,coalesce(baseline_snapshot,''),overlay_paths_json,created_at,duration_ms from comparisons where task_id=? order by created_at,rowid`
	rows, err := r.db.QueryContext(ctx, query, taskID)
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

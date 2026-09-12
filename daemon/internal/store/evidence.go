package store

import (
	"context"
)

type TestChange struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	PhaseID        string `json:"phase_id"`
	Attempt        int    `json:"attempt"`
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	Path           string `json:"path"`
	Reason         string `json:"reason"`
	ChangeKind     string `json:"change_kind"`
	RenameFrom     string `json:"rename_from,omitempty"`
	RenameTo       string `json:"rename_to,omitempty"`
	CreatedAt      string `json:"created_at"`
}
type Comparison struct {
	ID               string   `json:"id"`
	TaskID           string   `json:"task_id"`
	PhaseID          string   `json:"phase_id"`
	Attempt          int      `json:"attempt"`
	RepositoryID     string   `json:"repository_id"`
	RepositoryName   string   `json:"repository_name"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	BaselineSnapshot string   `json:"baseline_snapshot,omitempty"`
	OverlayPaths     []string `json:"overlay_paths"`
	CreatedAt        string   `json:"created_at"`
	DurationMS       int      `json:"duration_ms"`
}

func (db *DB) SaveTestChanges(ctx context.Context, changes []TestChange) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin test-change evidence", err)
	}
	defer tx.Rollback()
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `insert or replace into test_changes(id,task_id,phase_id,attempt,repository_id,repository_name,path,reason,change_kind,rename_from,rename_to,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, change.ID, change.TaskID, change.PhaseID, change.Attempt, change.RepositoryID, change.RepositoryName, change.Path, change.Reason, change.ChangeKind, nullIfEmpty(change.RenameFrom), nullIfEmpty(change.RenameTo), change.CreatedAt); err != nil {
			return wrap("save test-change evidence", err)
		}
	}
	return wrap("commit test-change evidence", tx.Commit())
}
func (db *DB) TestChanges(ctx context.Context, taskID string) ([]TestChange, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,phase_id,attempt,repository_id,repository_name,path,reason,change_kind,coalesce(rename_from,''),coalesce(rename_to,''),created_at from test_changes where task_id=? order by created_at,rowid`, taskID)
	if err != nil {
		return nil, wrap("read test-change evidence", err)
	}
	defer rows.Close()
	values := make([]TestChange, 0)
	for rows.Next() {
		var value TestChange
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt, &value.RepositoryID, &value.RepositoryName, &value.Path, &value.Reason, &value.ChangeKind, &value.RenameFrom, &value.RenameTo, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (db *DB) Comparisons(ctx context.Context, taskID string) ([]Comparison, error) {
	return []Comparison{}, nil
}

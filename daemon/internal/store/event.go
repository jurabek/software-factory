package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

type Event struct {
	Sequence         int64           `json:"sequence"`
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	PhaseID          string          `json:"phase_id,omitempty"`
	AttemptID        string          `json:"attempt_id,omitempty"`
	ArtifactID       string          `json:"artifact_id,omitempty"`
	BranchID         string          `json:"branch_id,omitempty"`
	ParentEventID    string          `json:"parent_event_id,omitempty"`
	Kind             session.Kind    `json:"kind"`
	FormatVersion    int             `json:"format_version"`
	Name             string          `json:"name,omitempty"`
	Payload          any             `json:"payload"`
	Display          session.Display `json:"display"`
	AvailableActions []string        `json:"available_actions,omitempty"`
	TokenCount       int             `json:"token_count,omitempty"`
	StartedAt        time.Time       `json:"started_at"`
	EndedAt          *time.Time      `json:"ended_at,omitempty"`
}

func (db *DB) AppendEvent(ctx context.Context, taskDir string, event Event) (int64, error) {
	if event.FormatVersion == 0 {
		event.FormatVersion = session.FormatVersion
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
	}
	display, err := json.Marshal(event.Display)
	if err != nil {
		return 0, fmt.Errorf("marshal event display: %w", err)
	}
	started := event.StartedAt.UTC().Format(time.RFC3339Nano)
	var ended any
	if event.EndedAt != nil {
		ended = event.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	actions, _ := json.Marshal(event.AvailableActions)
	if string(actions) == "null" {
		actions = []byte("[]")
	}
	result, err := db.ExecContext(ctx, `insert into events (id,task_id,phase_id,parent_event_id,kind,format_version,name,payload_json,display_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, nullIfEmpty(event.PhaseID), nullIfEmpty(event.ParentEventID), event.Kind, event.FormatVersion, nullIfEmpty(event.Name), string(payload), string(display), event.TokenCount, started, ended, nullIfEmpty(event.AttemptID), nullIfEmpty(event.ArtifactID), nullIfEmpty(event.BranchID), string(actions))
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	sequence, _ := result.LastInsertId()
	event.Sequence = sequence
	line, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(filepath.Join(taskDir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open event trace: %w", err)
	}
	defer file.Close()
	if _, err = file.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	return sequence, file.Sync()
}
func (db *DB) Events(ctx context.Context, taskID string, after int64, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and sequence>? order by sequence limit ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (db *DB) RecentEvents(ctx context.Context, taskID string, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,phase_id,parent_event_id,kind,format_version,name,payload_json,display_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json from (select sequence,id,task_id,coalesce(phase_id,'') as phase_id,coalesce(parent_event_id,'') as parent_event_id,kind,format_version,coalesce(name,'') as name,payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,'') as attempt_id,coalesce(artifact_id,'') as artifact_id,coalesce(branch_id,'') as branch_id,coalesce(actions_json,'[]') as actions_json from events where task_id=? order by sequence desc limit ?) order by sequence`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (db *DB) EventByID(ctx context.Context, taskID, eventID string) (Event, error) {
	var event Event
	var payload, display, started, actions string
	var ended sql.NullString
	err := db.QueryRowContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and id=?`, taskID, eventID).Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, wrap("read event", err)
	}
	if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
		return Event{}, wrap("decode event payload", err)
	}
	if err := json.Unmarshal([]byte(display), &event.Display); err != nil {
		return Event{}, wrap("decode event display", err)
	}
	_ = json.Unmarshal([]byte(actions), &event.AvailableActions)
	event.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if ended.Valid {
		value, _ := time.Parse(time.RFC3339Nano, ended.String)
		event.EndedAt = &value
	}
	return event, nil
}
func eventLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 250
	}
	return limit
}
func scanEvents(rows *sql.Rows) ([]Event, error) {
	values := make([]Event, 0)
	for rows.Next() {
		var event Event
		var payload, display, started, actions string
		var ended sql.NullString
		if err := rows.Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		if err := json.Unmarshal([]byte(display), &event.Display); err != nil {
			return nil, fmt.Errorf("decode event display: %w", err)
		}
		_ = json.Unmarshal([]byte(actions), &event.AvailableActions)
		event.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if ended.Valid {
			value, _ := time.Parse(time.RFC3339Nano, ended.String)
			event.EndedAt = &value
		}
		values = append(values, event)
	}
	return values, rows.Err()
}

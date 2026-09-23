package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	BranchID         string          `json:"branch_id,omitempty"`
	ParentEventID    string          `json:"parent_event_id,omitempty"`
	Kind             session.Kind    `json:"kind"`
	FormatVersion    int             `json:"format_version"`
	Name             string          `json:"name,omitempty"`
	NativeEntryID    string          `json:"native_entry_id,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	Payload          any             `json:"payload"`
	Display          session.Display `json:"display"`
	AvailableActions []string        `json:"available_actions,omitempty"`
	TokenCount       int             `json:"token_count,omitempty"`
	StartedAt        time.Time       `json:"started_at"`
	EndedAt          *time.Time      `json:"ended_at,omitempty"`
}

func (db *DB) AppendEvent(ctx context.Context, taskDir string, event Event) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin event append: %w", err)
	}
	defer tx.Rollback()
	sequence, err := appendEventTx(ctx, tx, event)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit event: %w", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		// The database is authoritative; trace export is derived output.
		slog.Error("write derived event trace", "task_id", event.TaskID, "event_id", event.ID, "error", err)
	}
	return sequence, nil
}

type eventInserter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendEventTx(ctx context.Context, inserter eventInserter, event Event) (int64, error) {
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
	result, err := inserter.ExecContext(ctx, `insert into events (id,task_id,phase_id,parent_event_id,kind,format_version,name,payload_json,display_json,native_entry_id,request_id,token_count,started_at,ended_at,attempt_id,branch_id,actions_json) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, nullIfEmpty(event.PhaseID), nullIfEmpty(event.ParentEventID), event.Kind, event.FormatVersion, nullIfEmpty(event.Name), string(payload), string(display), nullIfEmpty(event.NativeEntryID), nullIfEmpty(event.RequestID), event.TokenCount, started, ended, nullIfEmpty(event.AttemptID), nullIfEmpty(event.BranchID), string(actions))
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	sequence, _ := result.LastInsertId()
	return sequence, nil
}

func writeEventTrace(taskDir string, event Event, sequence int64) error {
	if event.FormatVersion == 0 {
		event.FormatVersion = session.FormatVersion
	}
	event.Sequence = sequence
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(taskDir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open event trace: %w", err)
	}
	defer file.Close()
	if _, err = file.Write(append(line, '\n')); err != nil {
		return err
	}
	return file.Sync()
}
func (db *DB) Events(ctx context.Context, taskID string, after int64, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and sequence>? order by sequence limit ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}
func (db *DB) RecentEvents(ctx context.Context, taskID string, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,phase_id,parent_event_id,kind,format_version,name,native_entry_id,request_id,payload_json,display_json,token_count,started_at,ended_at,attempt_id,branch_id,actions_json from (select sequence,id,task_id,coalesce(phase_id,'') as phase_id,coalesce(parent_event_id,'') as parent_event_id,kind,format_version,coalesce(name,'') as name,coalesce(native_entry_id,'') as native_entry_id,coalesce(request_id,'') as request_id,payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,'') as attempt_id,coalesce(branch_id,'') as branch_id,coalesce(actions_json,'[]') as actions_json from events where task_id=? order by sequence desc limit ?) order by sequence`, taskID, limit)
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
	err := db.QueryRowContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and id=?`, taskID, eventID).Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &event.NativeEntryID, &event.RequestID, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.BranchID, &actions)
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

// EventsByRequest returns the agent-derived events recorded for a factory
// request in append order.
func (db *DB) EventsByRequest(ctx context.Context, taskID, requestID string) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),coalesce(native_entry_id,''),coalesce(request_id,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and request_id=? order by sequence`, taskID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// EventNativeLink pairs a persisted event with its authoritative native entry.
type EventNativeLink struct {
	EventID       string
	NativeEntryID string
}

// SetEventNativeEntries backfills native entry references for a request's
// events. It is the write half of the reference index; payloads are resolved
// from the native session at read time.
func (db *DB) SetEventNativeEntries(ctx context.Context, taskID string, links []EventNativeLink) error {
	if len(links) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin native link: %w", err)
	}
	defer tx.Rollback()
	for _, link := range links {
		if link.EventID == "" || link.NativeEntryID == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `update events set native_entry_id=? where task_id=? and id=?`, link.NativeEntryID, taskID, link.EventID); err != nil {
			return fmt.Errorf("link native entry: %w", err)
		}
	}
	return wrap("commit native links", tx.Commit())
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
		if err := rows.Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &event.NativeEntryID, &event.RequestID, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.BranchID, &actions); err != nil {
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

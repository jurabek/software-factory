package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenRejectsLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`create table campaigns (id text primary key)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if !errors.Is(err, ErrStateIncompatible) {
		t.Fatalf("err = %v, want state incompatibility", err)
	}
}

func TestOpenAddsTaskSessionRelationship(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	previousSchema := strings.Replace(schema, " id text primary key, parent_task_id text references tasks(id) on delete cascade,\n", " id text primary key,\n", 1)
	if _, err = raw.Exec(previousSchema); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: "2026-09-05T00:00:00Z"}
	if err = db.CreateTask(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	session := Task{ID: "session-1", ParentTaskID: root.ID, Request: "Session", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: "2026-09-05T00:01:00Z"}
	if err = db.CreateTask(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	sessions, err := db.TaskSessions(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != root.ID || sessions[1].ID != session.ID {
		t.Fatalf("sessions = %#v", sessions)
	}
}

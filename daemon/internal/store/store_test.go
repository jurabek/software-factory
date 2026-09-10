package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	_ "modernc.org/sqlite"
)

func TestReserveAgentSessionConcurrentCallersShareWinner(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateTask(ctx, Task{ID: "task", Request: "request", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	const callers = 8
	results := make([]AgentSession, callers)
	errorsFound := make([]error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsFound[index] = db.ReserveAgentSession(ctx, "task", AgentSession{Role: "planner", Harness: "pi", HarnessSessionID: fmt.Sprintf("session-%d", index), SessionDirectory: "/tmp/session", AccountingComplete: true})
		}(index)
	}
	wait.Wait()
	winner := results[0].HarnessSessionID
	for index := range results {
		if errorsFound[index] != nil {
			t.Fatalf("caller %d: %v", index, errorsFound[index])
		}
		if results[index].HarnessSessionID != winner {
			t.Fatalf("caller %d session = %q, want %q", index, results[index].HarnessSessionID, winner)
		}
	}
}

func TestRepositoryReviewBaseAndPhaseGitInputsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task", Request: "request", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Repositories: []TaskRepository{{ID: "repo", TaskID: "task", Name: "app", SourceType: "local", SourceValue: "/source", Primary: true, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}}); err != nil {
		t.Fatal(err)
	}
	repository := TaskRepository{ID: "repo", TaskID: "task", CanonicalPath: "/source", WorkingPath: "/work", BaseSHA: "base", ReviewBaseSHA: "base", BranchName: "software-factory/task", Primary: true}
	if err = db.SetRepositoryPrepared(ctx, repository); err != nil {
		t.Fatal(err)
	}
	stored, err := db.TaskRepositories(ctx, "task")
	if err != nil || len(stored) != 1 || stored[0].ReviewBaseSHA != "base" || stored[0].BranchName != repository.BranchName {
		t.Fatalf("repository = %#v, err = %v", stored, err)
	}
	if err = db.AddPhase(ctx, Phase{ID: "attempt", TaskID: "task", Sequence: 1, Name: "planning", Kind: "agent", Owner: "planner", Status: "running", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	input := PhaseRepositoryInput{PhaseID: "attempt", RepositoryID: "repo", ReviewBaseSHA: "base", HeadSHA: "head", BranchName: repository.BranchName}
	if err = db.SavePhaseRepositoryInputs(ctx, "attempt", []PhaseRepositoryInput{input}); err != nil {
		t.Fatal(err)
	}
	inputs, err := db.PhaseRepositoryInputs(ctx, "attempt")
	if err != nil || len(inputs) != 1 || inputs[0] != input {
		t.Fatalf("phase inputs = %#v, err = %v", inputs, err)
	}
	if err = db.AdvanceReviewBase(ctx, "task", "repo", "wrong", "new"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected review base mismatch error: %v", err)
	}
	if err = db.AdvanceReviewBase(ctx, "task", "repo", "base", "new"); err != nil {
		t.Fatal(err)
	}
	stored, err = db.TaskRepositories(ctx, "task")
	if err != nil || stored[0].ReviewBaseSHA != "new" {
		t.Fatalf("advanced repository = %#v, err = %v", stored, err)
	}
}

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

func TestOpenRejectsMissingEventContractColumns(t *testing.T) {
	tests := []struct {
		name        string
		definition  string
		replacement string
	}{
		{name: "kind", definition: "parent_event_id text, kind text not null,", replacement: "parent_event_id text,"},
		{name: "format version", definition: "kind text not null, format_version integer not null default 1,", replacement: "kind text not null,"},
		{name: "display", definition: "payload_json text not null, display_json text not null default '{}',", replacement: "payload_json text not null,"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "factory.db")
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			incompatible := strings.Replace(schema, test.definition, test.replacement, 1)
			if incompatible == schema {
				t.Fatalf("event column definition %q not found", test.definition)
			}
			if _, err = raw.Exec(incompatible); err != nil {
				t.Fatal(err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}

			_, err = Open(path)
			if !errors.Is(err, ErrStateIncompatible) {
				t.Fatalf("err = %v, want state incompatibility", err)
			}
		})
	}
}

func TestEventContractRoundTrip(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	createdAt := "2026-09-08T00:00:00Z"
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "draft", CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, time.September, 8, 1, 2, 3, 4, time.UTC)
	endedAt := startedAt.Add(2 * time.Second)
	first := Event{
		ID:               "event-1",
		TaskID:           "task-1",
		PhaseID:          "phase-1",
		AttemptID:        "attempt-1",
		ArtifactID:       "artifact-1",
		BranchID:         "branch-1",
		ParentEventID:    "parent-1",
		Kind:             session.KindToolCall,
		Name:             "Read",
		Payload:          map[string]any{"path": "README.md"},
		Display:          session.Display{Role: "tool", Status: "success", Title: "Read", Target: "README.md", Result: "contents", Preview: "contents", DurationMS: 2000},
		AvailableActions: []string{"retry"},
		TokenCount:       12,
		StartedAt:        startedAt,
		EndedAt:          &endedAt,
	}
	if _, err = db.AppendEvent(ctx, taskDir, first); err != nil {
		t.Fatal(err)
	}
	second := Event{
		ID:            "event-2",
		TaskID:        "task-1",
		Kind:          session.KindMessage,
		FormatVersion: 7,
		Payload:       map[string]any{"role": "assistant", "text": "complete"},
		Display:       session.Display{Role: "agent", Status: "neutral", Title: "Agent response", Result: "complete", Preview: "complete"},
		StartedAt:     startedAt.Add(time.Minute),
	}
	if _, err = db.AppendEvent(ctx, taskDir, second); err != nil {
		t.Fatal(err)
	}

	events, err := db.Events(ctx, "task-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2", len(events))
	}
	assertEventContract(t, events[0], first, session.FormatVersion)
	assertEventContract(t, events[1], second, second.FormatVersion)

	recent, err := db.RecentEvents(ctx, "task-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent events count = %d, want 1", len(recent))
	}
	assertEventContract(t, recent[0], second, second.FormatVersion)

	byID, err := db.EventByID(ctx, "task-1", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEventContract(t, byID, first, session.FormatVersion)

	trace, err := os.ReadFile(filepath.Join(taskDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var mirrored Event
	if err = json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(trace)), "\n")[0]), &mirrored); err != nil {
		t.Fatal(err)
	}
	assertEventContract(t, mirrored, first, session.FormatVersion)
}

func TestAgentInvocationFinalizationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: "2026-09-08T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	reserved, err := db.ReserveAgentSession(ctx, "task-1", AgentSession{Role: "builder", Harness: "pi", HarnessSessionID: "018f0f79-9f0a-7d02-8c44-214f711a6c47", SessionDirectory: "/tmp/session", AccountingComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if reserved.SessionReady {
		t.Fatal("reserved session must not be ready")
	}
	if err := db.BeginAgentInvocation(ctx, "task-1", "builder", "invocation-1"); err != nil {
		t.Fatal(err)
	}
	pending, err := db.AgentSession(ctx, "task-1", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if pending.AccountingComplete {
		t.Fatal("pending invocation must expose incomplete accounting")
	}
	update := AgentSession{HarnessSessionID: reserved.HarnessSessionID, Provider: "github-copilot", Model: "model", SessionReady: true, Usage: session.Usage{Input: 10, Output: 5, TotalTokens: 15}, Cost: 1.25, AccountingComplete: true}
	if err := db.FinalizeAgentInvocation(ctx, "task-1", "builder", "invocation-1", update); err != nil {
		t.Fatal(err)
	}
	if err := db.FinalizeAgentInvocation(ctx, "task-1", "builder", "invocation-1", update); err != nil {
		t.Fatal(err)
	}
	stored, err := db.AgentSession(ctx, "task-1", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SessionReady || !stored.AccountingComplete || stored.Cost != 1.25 || stored.Usage.TotalTokens != 15 {
		t.Fatalf("session = %#v", stored)
	}
	task, err := db.Task(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.TotalCost != 1.25 {
		t.Fatalf("total cost = %v", task.TotalCost)
	}
}

func TestOpenRecoversPendingAgentInvocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factory.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: "2026-09-08T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReserveAgentSession(ctx, "task-1", AgentSession{Role: "planner", Harness: "pi", HarnessSessionID: "018f0f79-9f0a-7d02-8c44-214f711a6c48", SessionDirectory: "/tmp/session", AccountingComplete: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.BeginAgentInvocation(ctx, "task-1", "planner", "lost-invocation"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.AgentSession(ctx, "task-1", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccountingComplete || stored.PendingInvocationID != "" {
		t.Fatalf("recovered session = %#v", stored)
	}
}

func assertEventContract(t *testing.T, got, want Event, formatVersion int) {
	t.Helper()
	if got.Kind != want.Kind {
		t.Errorf("kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.FormatVersion != formatVersion {
		t.Errorf("format version = %d, want %d", got.FormatVersion, formatVersion)
	}
	if !reflect.DeepEqual(got.Display, want.Display) {
		t.Errorf("display = %#v, want %#v", got.Display, want.Display)
	}
}

package store

import (
	"context"
	"crypto/sha256"
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

func TestCreateTaskAllowsIndependentActiveTasks(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	first := Task{ID: "task-1", Request: "first", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: startedAt, StartedAt: startedAt}
	if err = db.CreateTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Task(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.StartedAt != startedAt {
		t.Fatalf("started at = %q, want %q", stored.StartedAt, startedAt)
	}
	second := Task{ID: "task-2", Request: "second", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: startedAt, StartedAt: startedAt}
	if err = db.CreateTask(ctx, second); err != nil {
		t.Fatalf("second active task error = %v", err)
	}
	if _, err = db.Task(ctx, second.ID); err != nil {
		t.Fatalf("second task lookup error = %v", err)
	}
}

func TestWorkspaceOperationRoundTripAndRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err = db.CreateTask(ctx, Task{ID: "task", Request: "request", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: createdAt}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	operation := WorkspaceOperation{ID: "operation", TaskID: "task", Kind: "materialize", Status: "running", RequestJSON: `{"path":"repo"}`, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err = db.CreateWorkspaceOperation(ctx, operation); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.WorkspaceOperation(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "interrupted" || stored.TaskID != operation.TaskID || stored.RequestJSON != operation.RequestJSON {
		t.Fatalf("workspace operation = %#v", stored)
	}
	if err = db.UpdateWorkspaceOperation(ctx, operation.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	stored, err = db.WorkspaceOperation(ctx, operation.ID)
	if err != nil || stored.Status != "succeeded" || stored.Error != "" {
		t.Fatalf("updated workspace operation = %#v, err = %v", stored, err)
	}
}

func TestReserveAgentSessionConcurrentCallersShareWinner(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateTask(ctx, Task{ID: "task", Request: "request", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
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

func TestRepositoryFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := Task{ID: "task", Request: "request", WorkspacePath: t.TempDir(), RepositoryType: "local", RepositorySource: "/source", State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `update tasks set canonical_repository_path=?,repository_path=?,base_sha=?,review_base_sha=?,branch_name=? where id=?`, "/source", "/work", "base", "base", "software-factory/task", task.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Task(ctx, task.ID)
	if err != nil || stored.ReviewBaseSHA != "base" || stored.BranchName != "software-factory/task" || stored.RepositoryPath != "/work" {
		t.Fatalf("task = %#v, err = %v", stored, err)
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
	root := Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: "2026-09-05T00:00:00Z"}
	if err = db.CreateTask(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	session := Task{ID: "session-1", ParentTaskID: root.ID, Request: "Session", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: "2026-09-05T00:01:00Z"}
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
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "preparing", CreatedAt: createdAt}); err != nil {
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

func TestEventCommitSurvivesDerivedTraceFailure(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", State: "preparing", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	taskDir := filepath.Join(t.TempDir(), "trace-file")
	if err = os.WriteFile(taskDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	sequence, err := db.AppendEvent(ctx, taskDir, Event{ID: "event-1", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "persisted"}, StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("append event = %v, want committed despite trace failure", err)
	}
	if sequence == 0 {
		t.Fatal("event sequence is zero")
	}
	events, err := db.Events(ctx, "task-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "event-1" {
		t.Fatalf("events = %+v, want committed event", events)
	}
}

func TestPhaseStartAndEventRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", State: "building", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateBranch(ctx, Branch{ID: "branch-1", TaskID: "task-1", Status: "active", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	phase := Phase{ID: "phase-1", TaskID: "task-1", Sequence: 1, Name: "build", Kind: "build", Owner: "builder", Attempt: 1, BranchID: "branch-1"}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	err = db.StartPhaseWithEvent(ctx, taskDir, phase, "building", Event{ID: "duplicate", TaskID: "task-1", PhaseID: phase.ID, Kind: session.KindPhaseStart, Payload: session.PhasePayload{Phase: phase.ID}, StartedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("phase start unexpectedly succeeded with duplicate event")
	}
	if _, err = db.PhaseByID(ctx, "task-1", phase.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("phase lookup error = %v, want not found", err)
	}
	task, err := db.Task(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ActivePhase != "" {
		t.Fatalf("active phase = %q, want empty", task.ActivePhase)
	}
	branch, err := db.Branch(ctx, "task-1", "branch-1")
	if err != nil {
		t.Fatal(err)
	}
	if branch.HeadAttemptID != "" {
		t.Fatalf("branch head = %q, want empty", branch.HeadAttemptID)
	}
}

func TestMessageDeliveryAndEventRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "building", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ReserveAgentSession(ctx, "task-1", AgentSession{StageID: "builder", Role: "builder", Harness: "test", HarnessSessionID: "session-1", SessionDirectory: taskDir, AccountingComplete: true}); err != nil {
		t.Fatal(err)
	}
	message, created, err := db.SaveMessage(ctx, Message{ID: "message-1", TaskID: "task-1", Actor: "user", Text: "continue", IdempotencyKey: "key-1", StageID: "builder", RecipientRole: "builder", AgentSessionID: "session-1", DeliveryStatus: "queued", CreatedAt: now()})
	if err != nil || !created {
		t.Fatalf("save message: created=%v err=%v", created, err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindTaskMessage, Payload: session.TaskMessagePayload{MessageID: message.ID, TaskID: message.TaskID, Text: message.Text, RecipientRole: "builder", AgentSessionID: "session-1", DeliveryStatus: "delivered"}, Display: session.Display{Role: "user", Status: "neutral", Title: "Message delivered"}, StartedAt: time.Now().UTC()}
	if err = db.BeginMessageInvocationWithEvent(ctx, "task-1", "builder", "invocation-1", message.ID, event, taskDir); err == nil {
		t.Fatal("delivery unexpectedly succeeded with duplicate event")
	}
	storedMessage, err := db.MessageByIdempotencyKey(ctx, "task-1", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if storedMessage.DeliveryStatus != "queued" {
		t.Fatalf("message status = %q, want queued", storedMessage.DeliveryStatus)
	}
	storedSession, err := db.AgentSession(ctx, "task-1", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if storedSession.PendingInvocationID != "" {
		t.Fatalf("pending invocation = %q, want empty", storedSession.PendingInvocationID)
	}
	events, err := db.Events(ctx, "task-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestMessageAcceptanceAndTaskLifecycleRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "awaiting_plan_approval", PlanDigest: "approved", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `update tasks set plan_digest='approved' where id='task-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	message := Message{ID: "message-1", TaskID: "task-1", Actor: "user", Text: "change plan", IdempotencyKey: "key-1", StageID: "planner", RecipientRole: "planner", AgentSessionID: "session-1", DeliveryStatus: "queued", CreatedAt: now()}
	event := Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindTaskMessage, Payload: session.TaskMessagePayload{MessageID: message.ID, TaskID: message.TaskID, DeliveryStatus: "queued"}, StartedAt: time.Now().UTC()}
	if _, _, err = db.AcceptMessageWithEvent(ctx, message, event, true, true, "planning", taskDir); err == nil {
		t.Fatal("message acceptance unexpectedly succeeded with duplicate event")
	}
	if _, err = db.MessageByIdempotencyKey(ctx, message.TaskID, message.IdempotencyKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message lookup error = %v, want not found", err)
	}
	task, err := db.Task(ctx, message.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != "awaiting_plan_approval" || task.PlanDigest != "approved" {
		t.Fatalf("task = %+v, want unchanged approval state", task)
	}
}

func TestMessageFailureAndEventRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "building", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.SaveMessage(ctx, Message{ID: "message-1", TaskID: "task-1", Actor: "user", Text: "continue", IdempotencyKey: "key-1", DeliveryStatus: "delivered", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindTaskMessage, Payload: session.TaskMessagePayload{MessageID: "message-1", TaskID: "task-1", DeliveryStatus: "failed", FailureReason: "harness_error"}, Display: session.Display{Role: "system", Status: "error", Title: "Message failed"}, StartedAt: time.Now().UTC()}
	if _, err = db.FailMessageWithEvent(ctx, "task-1", "message-1", "harness_error", event, taskDir); err == nil {
		t.Fatal("message failure unexpectedly succeeded with duplicate event")
	}
	stored, err := db.MessageByIdempotencyKey(ctx, "task-1", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeliveryStatus != "delivered" || stored.FailureReason != "" {
		t.Fatalf("message = %+v, want unchanged delivered message", stored)
	}
}

func TestPhaseCompletionAndTransitionRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "checking", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.AddPhase(ctx, Phase{ID: "phase-1", TaskID: "task-1", Sequence: 1, Name: "check", Kind: "verify", Owner: "factory", Status: "running", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "duplicate", TaskID: "task-1", PhaseID: "phase-1", AttemptID: "phase-1", Kind: session.KindPhaseEnd, Payload: session.PhasePayload{Phase: "phase-1", Status: "success"}, Display: session.Display{Role: "system", Status: "success", Title: "Phase complete"}, StartedAt: time.Now().UTC()}
	if err = db.CompletePhaseWithTransitionAndEvent(ctx, taskDir, "phase-1", "task-1", "checking", "reviewing", "success", "", event); err == nil {
		t.Fatal("phase completion unexpectedly succeeded with duplicate event")
	}
	task, err := db.Task(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != "checking" {
		t.Fatalf("task state = %q, want checking", task.State)
	}
	phase, err := db.PhaseByID(ctx, "task-1", "phase-1")
	if err != nil {
		t.Fatal(err)
	}
	if phase.Status != "running" {
		t.Fatalf("phase status = %q, want running", phase.Status)
	}
	events, err := db.Events(ctx, "task-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestApprovalAndLifecycleEventRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "awaiting_plan_approval", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `update tasks set plan_digest='candidate' where id='task-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Name: "task_approved", Payload: map[string]string{"digest": "approved"}, StartedAt: time.Now().UTC()}
	if err = db.ApproveWithEvent(ctx, taskDir, "task-1", "approved", "operator", event); err == nil {
		t.Fatal("approval unexpectedly succeeded with duplicate event")
	}
	task, err := db.Task(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != "awaiting_plan_approval" || task.PlanDigest != "candidate" || task.ApprovalActor != "" {
		t.Fatalf("task = %+v, want unchanged approval state", task)
	}
}

func TestPhaseReportPublicationRollsBackWithLifecycleEvent(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "planning", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.AddPhase(ctx, Phase{ID: "phase-1", TaskID: "task-1", Sequence: 1, Name: "planning", Kind: "agent", Owner: "planner", Status: "running", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	content := "# Plan\n\nAtomic."
	artifact := Artifact{ID: "report-1", TaskID: "task-1", AttemptID: "phase-1", Type: "plan_report", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content))), Content: content, MediaType: "text/markdown", Producer: "planner", CreatedAt: now()}
	event := Event{ID: "duplicate", TaskID: "task-1", PhaseID: "phase-1", AttemptID: "phase-1", Kind: session.KindPhaseEnd, Payload: session.PhasePayload{Phase: "phase-1", Status: "success"}, StartedAt: time.Now().UTC()}
	if err = db.CompletePhaseWithArtifactAndTransitionAndEvent(ctx, taskDir, "phase-1", "task-1", "planning", "awaiting_plan_approval", "success", "", &artifact, event); err == nil {
		t.Fatal("report publication unexpectedly succeeded with duplicate event")
	}
	if _, err = db.Artifact(ctx, "task-1", "report-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("artifact lookup error = %v, want not found", err)
	}
	task, err := db.Task(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != "planning" {
		t.Fatalf("task state = %q, want planning", task.State)
	}
}

func TestVerificationEvidenceAndPhaseTransitionRollbackTogether(t *testing.T) {
	ctx := context.Background()
	taskDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: taskDir, State: "checking", CreatedAt: now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.AddPhase(ctx, Phase{ID: "phase-1", TaskID: "task-1", Sequence: 1, Name: "verify", Kind: "verify", Owner: "factory", Status: "running", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendEvent(ctx, taskDir, Event{ID: "duplicate", TaskID: "task-1", Kind: session.KindCustom, Payload: map[string]string{"value": "existing"}, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	check := Check{ID: "check-1", TaskID: "task-1", PhaseID: "phase-1", Name: "test", Command: "true", Attempt: 1, Status: "passed", ExitCode: 0}
	event := Event{ID: "duplicate", TaskID: "task-1", PhaseID: "phase-1", AttemptID: "phase-1", Kind: session.KindPhaseEnd, Payload: session.PhasePayload{Phase: "phase-1", Status: "success"}, StartedAt: time.Now().UTC()}
	if err = db.CompleteVerificationPhaseWithEvidenceAndEvent(ctx, taskDir, "phase-1", "task-1", "checking", "reviewing", "success", "", []Check{check}, nil, event); err == nil {
		t.Fatal("verification completion unexpectedly succeeded with duplicate event")
	}
	storedChecks, err := db.Checks(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(storedChecks) != 0 {
		t.Fatalf("checks = %+v, want rolled back", storedChecks)
	}
	phase, err := db.PhaseByID(ctx, "task-1", "phase-1")
	if err != nil {
		t.Fatal(err)
	}
	if phase.Status != "running" {
		t.Fatalf("phase status = %q, want running", phase.Status)
	}
}

func TestAgentInvocationFinalizationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: "2026-09-08T00:00:00Z"}); err != nil {
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
	if err := db.CreateTask(ctx, Task{ID: "task-1", Request: "Task", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: "2026-09-08T00:00:00Z"}); err != nil {
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

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

func TestCommitAgentPhaseLifecycleRollsBackEnvelopePhaseTaskAndEventTogether(t *testing.T) {
	db, task, phase := lifecycleFixture(t)
	defer db.Close()

	err := db.CommitAgentPhaseLifecycleWithEvidence(context.Background(), phase, "success", "", "output", &TaskTransition{
		TaskID: task.ID, FromState: "preparing", ToState: "completed",
	}, Envelope{
		ID: "envelope-planner", TaskID: task.ID, PhaseID: phase.ID, StageID: "planner", AgentRole: "planner",
		OutputType: "planner", Payload: `{"status":"success"}`, Valid: true, Attempt: 1, CreatedAt: now(),
	}, []TestChange{{
		ID: "test-change", TaskID: task.ID, PhaseID: phase.ID, Attempt: 1, RepositoryID: "repository",
		RepositoryName: "app", Path: "app_test.go", Reason: "coverage", ChangeKind: "modified", CreatedAt: now(),
	}}, Event{
		ID: "event-missing-task", TaskID: "missing-task", PhaseID: phase.ID,
		Kind: session.KindPhaseEnd, Payload: map[string]string{"status": "success"},
		Display: session.Display{}, StartedAt: time.Now().UTC(),
	}, filepath.Join(t.TempDir(), "task"))
	if err == nil {
		t.Fatal("expected lifecycle event foreign-key failure")
	}
	storedPhase, err := db.PhaseByID(context.Background(), task.ID, phase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedPhase.Status != "running" || storedPhase.OutputSnapshot != "" {
		t.Fatalf("phase was partially committed: %+v", storedPhase)
	}
	storedTask, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.State != "preparing" {
		t.Fatalf("task was partially committed: %+v", storedTask)
	}
	envelopes, err := db.Envelopes(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 0 {
		t.Fatalf("envelopes = %#v, want no envelope after rollback", envelopes)
	}
	changes, err := db.TestChanges(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("test changes = %#v, want no evidence after rollback", changes)
	}
	events, err := db.Events(context.Background(), task.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no event after rollback", events)
	}
}

func TestCommitPhaseStartRollsBackPhaseInputsAndEventTogether(t *testing.T) {
	db, task, phase := lifecycleFixture(t)
	defer db.Close()
	phase.ID = "phase-start"
	phase.Name = "building"
	phase.BranchID = "missing-branch"
	event := Event{ID: "event-start", TaskID: task.ID, PhaseID: phase.ID, Kind: session.KindPhaseStart, Payload: map[string]string{"status": "running"}, Display: session.Display{}, StartedAt: time.Now().UTC()}

	err := db.CommitPhaseStart(context.Background(), phase, []PhaseRepositoryInput{{PhaseID: phase.ID, RepositoryID: "missing-repository", ReviewBaseSHA: "base", HeadSHA: "head", BranchName: "branch"}}, event, filepath.Join(t.TempDir(), "task"))
	if err == nil {
		t.Fatal("expected phase branch foreign-key failure")
	}
	if _, err = db.PhaseByID(context.Background(), task.ID, phase.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("phase lookup error = %v, want rollback", err)
	}
	inputs, err := db.PhaseRepositoryInputs(context.Background(), phase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 0 {
		t.Fatalf("phase inputs = %#v, want rollback", inputs)
	}
	events, err := db.Events(context.Background(), task.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want rollback", events)
	}
}

func TestCommitApprovalRollsBackApprovalAndTaskTransitionTogether(t *testing.T) {
	db, task, _ := lifecycleFixture(t)
	defer db.Close()
	if err := db.Transition(context.Background(), task.ID, "preparing", "awaiting_plan_approval", "", ""); err != nil {
		t.Fatal(err)
	}

	err := db.CommitApproval(context.Background(), task.ID, "awaiting_plan_approval", "digest", "actor", Event{
		ID: "event-approval", TaskID: "missing-task", Kind: session.KindPlanFeedback,
		Payload: map[string]string{"feedback": "approved"}, Display: session.Display{}, StartedAt: time.Now().UTC(),
	}, filepath.Join(t.TempDir(), "task"))
	if err == nil {
		t.Fatal("expected approval event foreign-key failure")
	}
	storedTask, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.State != "awaiting_plan_approval" || storedTask.PlanDigest != "" || storedTask.ApprovalActor != "" {
		t.Fatalf("approval was partially committed: %+v", storedTask)
	}
}

func TestCommitMessageAcceptanceRollsBackMessageAndTaskChangeTogether(t *testing.T) {
	db, task, phase := lifecycleFixture(t)
	defer db.Close()
	if err := db.Transition(context.Background(), task.ID, "preparing", "awaiting_plan_approval", phase.ID, ""); err != nil {
		t.Fatal(err)
	}

	_, _, err := db.CommitMessageAcceptance(context.Background(), Message{
		ID: "message-1", TaskID: task.ID, Actor: "actor", Text: "continue", IdempotencyKey: "key-1",
		StageID: "planner", RecipientRole: "planner", AgentSessionID: "session-1", DeliveryStatus: "queued", CreatedAt: now(),
	}, "awaiting_plan_approval", "planning", true, Event{
		ID: "event-message", TaskID: "missing-task", Kind: session.KindTaskMessage,
		Payload: map[string]string{"text": "continue"}, Display: session.Display{}, StartedAt: time.Now().UTC(),
	}, filepath.Join(t.TempDir(), "task"))
	if err == nil {
		t.Fatal("expected message event foreign-key failure")
	}
	if _, err = db.MessageByIdempotencyKey(context.Background(), task.ID, "key-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message lookup error = %v, want rollback", err)
	}
	storedTask, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.State != "awaiting_plan_approval" {
		t.Fatalf("task was partially committed: %+v", storedTask)
	}
}

func TestCommitPhaseLifecycleKeepsDatabaseCommitWhenTraceExportFails(t *testing.T) {
	db, task, phase := lifecycleFixture(t)
	defer db.Close()
	tracePath := filepath.Join(t.TempDir(), "trace-is-a-file")
	if err := os.WriteFile(tracePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := db.CommitPhaseLifecycle(context.Background(), phase, "success", "", "output", &TaskTransition{
		TaskID: task.ID, FromState: "preparing", ToState: "completed",
	}, Event{
		ID: "event-export-failure", TaskID: task.ID, PhaseID: phase.ID,
		Kind: session.KindPhaseEnd, Payload: map[string]string{"status": "success"},
		Display: session.Display{}, StartedAt: time.Now().UTC(),
	}, tracePath); err != nil {
		t.Fatalf("committed lifecycle returned export failure: %v", err)
	}
	storedPhase, err := db.PhaseByID(context.Background(), task.ID, phase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedPhase.Status != "success" || storedPhase.OutputSnapshot != "output" {
		t.Fatalf("phase = %+v", storedPhase)
	}
	storedTask, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.State != "completed" {
		t.Fatalf("task = %+v", storedTask)
	}
	events, err := db.Events(context.Background(), task.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "event-export-failure" {
		t.Fatalf("events = %#v", events)
	}
}

func lifecycleFixture(t *testing.T) (*DB, Task, Phase) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "task-lifecycle", Request: "lifecycle", WorkspacePath: t.TempDir(), State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = db.CreateTask(context.Background(), task); err != nil {
		db.Close()
		t.Fatal(err)
	}
	phase := Phase{ID: "phase-lifecycle", TaskID: task.ID, Sequence: 1, Name: "prepare", Kind: "git", Owner: "factory", Status: "running", Attempt: 1}
	if err = db.AddPhase(context.Background(), phase); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, task, phase
}

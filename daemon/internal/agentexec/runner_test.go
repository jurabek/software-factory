package agentexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type errorScriptedHarness struct {
	requests []harness.Request
	results  []harness.Result
	errs     []error
}

func (h *errorScriptedHarness) Models(context.Context) ([]harness.Model, error) {
	return nil, nil
}

func (h *errorScriptedHarness) Run(_ context.Context, request harness.Request, _ harness.EventSink) (harness.Result, error) {
	h.requests = append(h.requests, request)
	result := h.results[0]
	h.results = h.results[1:]
	var runErr error
	if len(h.errs) > 0 {
		runErr = h.errs[0]
		h.errs = h.errs[1:]
	}
	return result, runErr
}

type liveEventFailureHarness struct {
	cancelled chan struct{}
}

func (h *liveEventFailureHarness) Models(context.Context) ([]harness.Model, error) {
	return nil, nil
}

func (h *liveEventFailureHarness) Run(ctx context.Context, request harness.Request, sink harness.EventSink) (harness.Result, error) {
	if err := sink(ctx, session.NewMessage(session.MessagePayload{Role: "assistant", Text: "required output"})); err == nil {
		return harness.Result{}, errors.New("live event persistence unexpectedly succeeded")
	}
	select {
	case <-ctx.Done():
		close(h.cancelled)
	case <-time.After(time.Second):
		return harness.Result{}, errors.New("invocation context was not cancelled after live event failure")
	}
	return harness.Result{SessionID: request.SessionID, SessionReady: true, AccountingComplete: true}, nil
}

func testTurnDeps(t *testing.T, adapter harness.Harness, fixAttempts int) (Deps, *store.DB, store.Task) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	task := store.Task{
		ID: "task-1", Request: "Build it", WorkspacePath: filepath.Join(root, "tasks", "task-1"),
		RepositoryType: "github", RepositorySource: "owner/repository", State: "building",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err = os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	deps := Deps{DB: db, Harnesses: harness.Registry{"pi": adapter}, JSONFixAttempts: fixAttempts}
	return deps, db, task
}

func validBuild(text string) (any, error) {
	if !strings.Contains(text, `"ok"`) {
		return nil, errors.New("missing envelope field")
	}
	return text, nil
}

func runTurn(t *testing.T, deps Deps, db *store.DB, task store.Task, phase store.Phase, role string, validate Validate) (string, error) {
	t.Helper()
	return RunTurn(context.Background(), deps, TurnInput{
		TaskID: task.ID, Phase: phase, Role: role, HarnessName: "pi",
		Model: "provider/model", Thinking: "low",
		SessionDir: filepath.Join(task.WorkspacePath, "sessions", role, "pi"),
		UserPrompt: "do it", SystemPrompt: "system",
		EnvelopeKind: role, CorrectionSuffix: "suffix",
		Validate: validate, Sink: testSink(db, task.ID, phase.ID),
	})
}

func testSink(db *store.DB, taskID, phaseID string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		_, err := db.AppendEvent(ctx, "", store.Event{
			ID: uuid.New().String(), TaskID: taskID, PhaseID: phaseID,
			Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
			StartedAt: time.Now().UTC(),
		})
		return err
	}
}

func TestRunTurnPersistsMetadataOnRunError(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{{Text: "boom", SessionReady: true, AccountingComplete: false, Usage: harness.Usage{Input: 10, Output: 5, TotalTokens: 15, Cost: 0.02}}},
		errs:    []error{errors.New("cli failed")},
	}
	deps, db, task := testTurnDeps(t, agent, 0)
	_, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild)
	if err == nil || !strings.Contains(err.Error(), "cli failed") {
		t.Fatalf("err = %v, want cli failed", err)
	}
	stored, err := db.AgentSession(context.Background(), task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SessionReady {
		t.Fatal("session_ready = false, want true after error result with readiness")
	}
	if stored.Cost != 0.02 {
		t.Fatalf("cost = %v, want 0.02", stored.Cost)
	}
	if stored.Usage.Input != 10 || stored.Usage.Output != 5 {
		t.Fatalf("usage = %+v, want input 10 output 5", stored.Usage)
	}
	if stored.AccountingComplete {
		t.Fatal("accounting_complete = true, want false")
	}
}

func TestRunTurnCancelsInvocationWhenRequiredLiveEventCannotPersist(t *testing.T) {
	agent := &liveEventFailureHarness{cancelled: make(chan struct{})}
	deps, db, task := testTurnDeps(t, agent, 0)
	ctx := context.Background()
	phase := store.Phase{ID: "phase-1", TaskID: task.ID, Sequence: 1, Name: "building", Kind: "agent", Owner: "builder", Status: "running", Attempt: 1}
	if err := db.AddPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `create trigger reject_required_live_event before insert on events when new.kind = 'message' begin select raise(abort, 'required live event failure'); end`); err != nil {
		t.Fatal(err)
	}

	_, err := runTurn(t, deps, db, task, phase, "builder", validBuild)
	if err == nil || !strings.Contains(err.Error(), "required live event failure") {
		t.Fatalf("err = %v, want required live event failure", err)
	}
	select {
	case <-agent.cancelled:
	default:
		t.Fatal("harness did not observe invocation cancellation")
	}
	envelopes, err := db.Envelopes(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 0 {
		t.Fatalf("envelopes = %+v, want none after required live event failure", envelopes)
	}
	stored, err := db.AgentSession(ctx, task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PendingInvocationID != "" || !stored.AccountingComplete {
		t.Fatalf("agent session = %+v, want settled invocation accounting", stored)
	}
}

func TestRunTurnCorrectionSetsResumeAfterInit(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{
			{Text: "invalid", SessionReady: true, AccountingComplete: true},
			{Text: `{"ok":true}`, SessionReady: true, AccountingComplete: true},
		},
	}
	deps, db, task := testTurnDeps(t, agent, 1)
	if _, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild); err != nil {
		t.Fatal(err)
	}
	if len(agent.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(agent.requests))
	}
	if agent.requests[0].Resume {
		t.Fatal("first request Resume = true, want false")
	}
	if !agent.requests[1].Resume {
		t.Fatal("correction request Resume = false, want true after initialization")
	}
}

func TestRunTurnPreservesReadyWhenFailedResultOmitsReadiness(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{
			{Text: `{"ok":true}`, SessionReady: true, AccountingComplete: true},
			{Text: "boom", SessionReady: false, AccountingComplete: false},
		},
		errs: []error{nil, errors.New("cli failed")},
	}
	deps, db, task := testTurnDeps(t, agent, 0)
	if _, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild); err != nil {
		t.Fatal(err)
	}
	_, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-2"}, "builder", validBuild)
	if err == nil {
		t.Fatal("expected second run error")
	}
	stored, err := db.AgentSession(context.Background(), task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SessionReady {
		t.Fatal("session_ready reset to false by failed result, want preserved true")
	}
}

func TestRunTurnSessionMismatchErrorsEvenWithRunError(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{{SessionID: "00000000-0000-0000-0000-000000000000", Text: "boom", SessionReady: true, AccountingComplete: false}},
		errs:    []error{errors.New("cli failed")},
	}
	deps, db, task := testTurnDeps(t, agent, 0)
	_, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if !strings.Contains(err.Error(), "cli failed") || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("err = %v, want both run and identity errors", err)
	}
	stored, err := db.AgentSession(context.Background(), task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if stored.HarnessSessionID == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("stored session identity changed to mismatched ID")
	}
	if !stored.SessionReady {
		t.Fatal("ready metadata not persisted alongside combined error")
	}
}

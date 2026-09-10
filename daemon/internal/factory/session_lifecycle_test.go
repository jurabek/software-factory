package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
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

func testRoleService(t *testing.T, agent harness.Harness, fixAttempts int) (*Service, store.Task) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	promptDir := filepath.Join(root, "prompts", "builder")
	if err = os.MkdirAll(promptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("Build."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(promptDir, "user.md"), []byte("Build {{.TaskID}}."), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Defaults: config.Defaults{CodingAgent: "pi", Model: "provider/model", Thinking: "low"},
		Runtime:  config.Runtime{JSONFixAttempts: fixAttempts},
		Agents: []config.Agent{{
			Name:     "builder",
			Model:    "provider/model",
			Thinking: "low",
			PromptEngineering: config.PromptEngineering{
				System: "prompts/builder/system.md",
				User:   "prompts/builder/user.md",
			},
		}},
	}
	service := NewService(root, db, cfg, filepath.Join(root, "config.yaml"), harness.Registry{"pi": agent}, nil)
	task, err := service.Create(context.Background(), CreateRequest{Request: "Build it", Repositories: []Repository{{Type: "github", Repo: "owner/repository"}}})
	if err != nil {
		t.Fatal(err)
	}
	return service, task
}

func validBuild(text string) (any, error) {
	if !strings.Contains(text, `"ok"`) {
		return nil, errors.New("missing envelope field")
	}
	return text, nil
}

func TestRunRolePersistsMetadataOnRunError(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{{Text: "boom", SessionReady: true, AccountingComplete: false, Usage: harness.Usage{Input: 10, Output: 5, TotalTokens: 15, Cost: 0.02}}},
		errs:    []error{errors.New("cli failed")},
	}
	service, task := testRoleService(t, agent, 0)
	_, err := service.runRole(context.Background(), task, store.Phase{ID: "phase-1"}, "builder", map[string]any{"TaskID": task.ID}, validBuild)
	if err == nil || !strings.Contains(err.Error(), "cli failed") {
		t.Fatalf("err = %v, want cli failed", err)
	}
	stored, err := service.db.AgentSession(context.Background(), task.ID, "builder")
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

func TestRunRoleCorrectionSetsResumeAfterInit(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{
			{Text: "invalid", SessionReady: true, AccountingComplete: true},
			{Text: `{"ok":true}`, SessionReady: true, AccountingComplete: true},
		},
	}
	service, task := testRoleService(t, agent, 1)
	if _, err := service.runRole(context.Background(), task, store.Phase{ID: "phase-1"}, "builder", map[string]any{"TaskID": task.ID}, validBuild); err != nil {
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

func TestRunRolePreservesReadyWhenFailedResultOmitsReadiness(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{
			{Text: `{"ok":true}`, SessionReady: true, AccountingComplete: true},
			{Text: "boom", SessionReady: false, AccountingComplete: false},
		},
		errs: []error{nil, errors.New("cli failed")},
	}
	service, task := testRoleService(t, agent, 0)
	if _, err := service.runRole(context.Background(), task, store.Phase{ID: "phase-1"}, "builder", map[string]any{"TaskID": task.ID}, validBuild); err != nil {
		t.Fatal(err)
	}
	_, err := service.runRole(context.Background(), task, store.Phase{ID: "phase-2"}, "builder", map[string]any{"TaskID": task.ID}, validBuild)
	if err == nil {
		t.Fatal("expected second run error")
	}
	stored, err := service.db.AgentSession(context.Background(), task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SessionReady {
		t.Fatal("session_ready reset to false by failed result, want preserved true")
	}
}

func TestRunRoleSessionMismatchErrorsEvenWithRunError(t *testing.T) {
	agent := &errorScriptedHarness{
		results: []harness.Result{{SessionID: "00000000-0000-0000-0000-000000000000", Text: "boom", SessionReady: true, AccountingComplete: false}},
		errs:    []error{errors.New("cli failed")},
	}
	service, task := testRoleService(t, agent, 0)
	_, err := service.runRole(context.Background(), task, store.Phase{ID: "phase-1"}, "builder", map[string]any{"TaskID": task.ID}, validBuild)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if !strings.Contains(err.Error(), "cli failed") || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("err = %v, want both run and identity errors", err)
	}
	stored, err := service.db.AgentSession(context.Background(), task.ID, "builder")
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

func TestRetryRotatesClaudeSessionPreservingCost(t *testing.T) {
	service, db, _ := testService(t)
	ctx := context.Background()
	task, phase := createTaskWithAttempt(t, service, db)
	claudeDir := filepath.Join(task.WorkspacePath, "sessions", "builder", "claude")
	original, err := db.ReserveAgentSession(ctx, task.ID, store.AgentSession{Role: "builder", Harness: "claude", Model: "anthropic/sonnet", Thinking: "medium", HarnessSessionID: uuid.New().String(), SessionDirectory: claudeDir, AccountingComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	piDir := filepath.Join(task.WorkspacePath, "sessions", "builder", "pi")
	_ = piDir
	result, err := service.Intervene(ctx, task.ID, "tester", InterveneRequest{Target: InterventionTarget{AttemptID: phase.ID}, Intent: "retry", IdempotencyKey: "rotate-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptID == "" {
		t.Fatal("expected new attempt")
	}
	rotated, err := db.AgentSession(ctx, task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.HarnessSessionID == original.HarnessSessionID {
		t.Fatal("claude session was not rotated on rewind")
	}
	if rotated.SessionReady {
		t.Fatal("rotated session must start unready")
	}
	_ = original
}

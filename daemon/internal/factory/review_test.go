package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type scriptedHarness struct {
	requests []harness.Request
	results  []harness.Result
}

func (h *scriptedHarness) Models(context.Context) ([]harness.Model, error) { return nil, nil }

func (h *scriptedHarness) Run(_ context.Context, request harness.Request, _ harness.EventSink) (harness.Result, error) {
	h.requests = append(h.requests, request)
	result := h.results[0]
	h.results = h.results[1:]
	return result, nil
}

func TestRunRoleProvidesReviewerEnvelopeContract(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	promptDir := filepath.Join(root, "prompts", "reviewer")
	if err = os.MkdirAll(promptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("Review without editing."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(promptDir, "user.md"), []byte("Review {{.TaskID}}."), 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &scriptedHarness{results: []harness.Result{
		{Text: `{"status":"approved","summary":"Looks good","findings":[]}`},
		{Text: `{"status":"success","summary":"Looks good","artifacts":[],"notes_for_next_agent":"","approved":true,"findings":[],"blocking":[]}`},
	}}
	cfg := config.Config{
		Defaults: config.Defaults{CodingAgent: "pi", Model: "provider/model", Thinking: "low"},
		Runtime:  config.Runtime{JSONFixAttempts: 1},
		Agents: []config.Agent{{
			Name:     "reviewer",
			Model:    "provider/model",
			Thinking: "low",
			PromptEngineering: config.PromptEngineering{
				System: "prompts/reviewer/system.md",
				User:   "prompts/reviewer/user.md",
			},
		}},
	}
	service := NewService(root, Dependencies{
		Store: db, Config: cfg, ConfigPath: filepath.Join(root, "config.yaml"),
		Harnesses: harness.Registry{"pi": agent},
	})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "Review change", Repository: Repository{Type: "github", Repo: "owner/repository"}}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.runRole(context.Background(), task, store.Phase{ID: "review-phase"}, "reviewer", map[string]any{"TaskID": task.ID}, func(text string) (any, error) { return ValidateReview(text) })
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(agent.requests))
	}
	for _, field := range []string{`"status"`, `"summary"`, `"artifacts"`, `"notes_for_next_agent"`, `"approved"`, `"findings"`, `"blocking"`} {
		if !strings.Contains(agent.requests[0].SystemPrompt, field) {
			t.Errorf("initial system prompt missing %s", field)
		}
		if !strings.Contains(agent.requests[1].Prompt, field) {
			t.Errorf("correction prompt missing %s", field)
		}
	}
	if !strings.Contains(agent.requests[1].Prompt, `missing envelope field "artifacts"`) {
		t.Fatalf("correction prompt missing validation error: %q", agent.requests[1].Prompt)
	}
}

func TestReadOnlyPhaseReusesUnchangedSnapshot(t *testing.T) {
	service, db, _ := testService(t)
	ctx := context.Background()
	task, err := service.tasks.create(ctx, CreateRequest{Request: "Review change", Repository: Repository{Type: "github", Repo: "owner/repository"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := store.Phase{ID: "checks-phase", TaskID: task.ID, Sequence: 1, Name: "checks", Kind: "check", Owner: "factory", Status: "success", Attempt: 1, OutputSnapshot: "checked-snapshot"}
	if err = db.AddPhase(ctx, previous); err != nil {
		t.Fatal(err)
	}

	review, err := service.beginPhase(ctx, task.ID, "reviewing", "agent", "reviewer", "Review implementation")
	if err != nil {
		t.Fatal(err)
	}
	if review.InputSnapshot != previous.OutputSnapshot {
		t.Fatalf("review input snapshot = %q, want %q", review.InputSnapshot, previous.OutputSnapshot)
	}
	if err = service.endPhase(ctx, review, "success", nil); err != nil {
		t.Fatal(err)
	}
	stored, err := db.PhaseByID(ctx, task.ID, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OutputSnapshot != review.InputSnapshot {
		t.Fatalf("review output snapshot = %q, want unchanged input %q", stored.OutputSnapshot, review.InputSnapshot)
	}
}

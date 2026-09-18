package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/intervention"
	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type testHarness struct{}

func (testHarness) Models(context.Context) ([]harness.Model, error) { return nil, nil }
func (testHarness) Run(_ context.Context, request harness.Request, _ harness.EventSink) (harness.Result, error) {
	return harness.Result{SessionID: request.SessionID, SessionReady: true, AccountingComplete: true}, nil
}

func TestMessagesAreIdempotentFIFOAndAbortFailsQueue(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	agents := make([]config.Agent, 0, 3)
	for _, role := range []string{"planner", "builder", "reviewer"} {
		directory := filepath.Join(root, "prompts", role)
		if err = os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, "system.md"), []byte("Work."), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, "user.md"), []byte("Task {{.TaskID}}."), 0o600); err != nil {
			t.Fatal(err)
		}
		agents = append(agents, config.Agent{Name: role, Model: "provider/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: filepath.Join("prompts", role, "system.md"), User: filepath.Join("prompts", role, "user.md")}})
	}
	cfg := config.Config{Defaults: config.Defaults{CodingAgent: "pi", Model: "provider/model", Thinking: "low"}, Agents: agents}
	configPath := filepath.Join(root, "config.yaml")
	registry := harness.Registry{"pi": testHarness{}}
	events := orchestrator.NewEvents(db)
	controller := orchestrator.New(root, orchestrator.Dependencies{Store: db, Events: events})
	t.Cleanup(func() { controller.Shutdown(context.Background()) })
	interventions := intervention.New(intervention.Deps{Store: db, Git: factorygit.OSRunner{}, Snapshots: workspace.New(db, factorygit.OSRunner{}), Config: cfg, ConfigPath: configPath, Root: root, Events: events})
	messages := New(Deps{Store: db, Config: cfg, ConfigPath: configPath, Harnesses: registry, Root: root, Interventions: interventions, Events: events})
	tasks := task.New(root, task.Deps{Store: db, Config: cfg, ConfigPath: configPath, Harnesses: registry})
	created, err := tasks.Create(context.Background(), task.CreateRequest{Request: "change", Repository: task.Repository{Type: "github", Repo: "owner/repository"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, _, err := messages.Send(ctx, created.ID, "tester", Request{Text: "abort this task", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := messages.Send(ctx, created.ID, "tester", Request{Text: "ignored duplicate", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := messages.Send(ctx, created.ID, "tester", Request{Text: "second", IdempotencyKey: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != duplicate.ID || first.Sequence >= second.Sequence {
		t.Fatalf("message ordering/idempotency = first %+v duplicate %+v second %+v", first, duplicate, second)
	}
	if first.RecipientRole != "planner" || first.AgentSessionID == "" || first.AgentSessionID != second.AgentSessionID {
		t.Fatalf("routing = first %+v second %+v", first, second)
	}
	if err = events.Publish(ctx, created.ID, store.TaskCancelled); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, taskErr := db.Task(ctx, created.ID)
		if taskErr != nil {
			t.Fatal(taskErr)
		}
		if current.State == string(stagekit.Aborted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task state = %q, want %q", current.State, stagekit.Aborted)
		}
		time.Sleep(time.Millisecond)
	}
	stored, err := db.Messages(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range stored {
		if message.DeliveryStatus != "failed" || message.FailureReason != "task_aborted" {
			t.Fatalf("aborted message = %+v", message)
		}
	}
	if _, _, err = messages.Send(ctx, created.ID, "tester", Request{Text: "abort this task", IdempotencyKey: "three"}); err == nil {
		t.Fatal("message accepted after abort")
	}
	if task, err := db.Task(ctx, created.ID); err != nil || task.State != string(stagekit.Aborted) {
		t.Fatalf("task = %+v, err = %v", task, err)
	}
}

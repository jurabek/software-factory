package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/messaging"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
)

type scriptedHarness struct {
	requests []harness.Request
	results  []harness.Result
}

func (h *scriptedHarness) Models(context.Context) ([]harness.Model, error) { return nil, nil }

func (h *scriptedHarness) Run(_ context.Context, request harness.Request, _ harness.EventSink) (harness.Result, error) {
	h.requests = append(h.requests, request)
	if len(h.results) == 0 {
		return harness.Result{SessionID: request.SessionID, SessionReady: true, AccountingComplete: true}, nil
	}
	result := h.results[0]
	h.results = h.results[1:]
	return result, nil
}

func messageTestService(t *testing.T, adapter harness.Harness) (*Service, *store.DB, store.Task) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
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
	service := New(root, Dependencies{
		Store: db, Config: cfg, ConfigPath: filepath.Join(root, "config.yaml"),
		Harnesses: harness.Registry{"pi": adapter},
	})
	created, err := service.tasks.Create(context.Background(), task.CreateRequest{Request: "change", Repository: task.Repository{Type: "github", Repo: "owner/repository"}})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, created
}

func TestMessagesAreIdempotentFIFOAndAbortFailsQueue(t *testing.T) {
	adapter := &scriptedHarness{}
	service, db, created := messageTestService(t, adapter)
	ctx := context.Background()
	first, err := service.SendMessage(ctx, created.ID, "tester", messaging.Request{Text: "abort this task", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.SendMessage(ctx, created.ID, "tester", messaging.Request{Text: "ignored duplicate", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SendMessage(ctx, created.ID, "tester", messaging.Request{Text: "second", IdempotencyKey: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != duplicate.ID || first.Sequence >= second.Sequence {
		t.Fatalf("message ordering/idempotency = first %+v duplicate %+v second %+v", first, duplicate, second)
	}
	if first.RecipientRole != "planner" || first.AgentSessionID == "" || first.AgentSessionID != second.AgentSessionID {
		t.Fatalf("routing = first %+v second %+v", first, second)
	}
	unchanged, err := db.Task(ctx, created.ID)
	if err != nil || unchanged.State != string(Preparing) {
		t.Fatalf("message executed control: task = %+v err = %v", unchanged, err)
	}
	if err = service.Abort(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := db.Messages(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.DeliveryStatus != "failed" || message.FailureReason != "task_aborted" {
			t.Fatalf("aborted message = %+v", message)
		}
	}
	if _, err = service.SendMessage(ctx, created.ID, "tester", messaging.Request{Text: "abort this task", IdempotencyKey: "three"}); err == nil {
		t.Fatal("message accepted after abort")
	}
}

func TestExecutionOwnerAdmitsOneSuccessorAfterCurrentSettlement(t *testing.T) {
	service, _, created := messageTestService(t, &scriptedHarness{})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	service.launch(created.ID, func(context.Context, string) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	})
	<-firstStarted
	service.launch(created.ID, func(context.Context, string) error {
		close(secondStarted)
		return nil
	})
	select {
	case <-secondStarted:
		t.Fatal("successor started before current execution settled")
	default:
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("successor was not admitted after current execution settled")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	service.Shutdown(shutdownCtx)
}

func TestAvailableActionsContainControlsOnly(t *testing.T) {
	for _, actions := range [][]string{
		stagekit.AvailableActions(nil, string(Preparing)),
		stagekit.AvailableActions(&store.Phase{Status: "running", Kind: "agent"}, string(Building)),
		stagekit.AvailableActions(&store.Phase{Status: "failed", Kind: "check"}, string(Blocked)),
	} {
		for _, action := range actions {
			switch action {
			case "approve", "pause", "resume", "abort", "retry":
			default:
				t.Fatalf("non-control action %q in %v", action, actions)
			}
		}
	}
}

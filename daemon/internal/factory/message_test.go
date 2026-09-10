package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

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
	service := NewService(root, Dependencies{
		Store: db, Config: cfg, ConfigPath: filepath.Join(root, "config.yaml"),
		Harnesses: harness.Registry{"pi": adapter},
	})
	task, err := service.Create(context.Background(), CreateRequest{Request: "change", Repositories: []Repository{{Type: "github", Repo: "owner/repository"}}})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, task
}

func TestMessagesAreIdempotentFIFOAndAbortFailsQueue(t *testing.T) {
	adapter := &scriptedHarness{}
	service, db, task := messageTestService(t, adapter)
	ctx := context.Background()
	first, err := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "abort this task", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "ignored duplicate", IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "second", IdempotencyKey: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != duplicate.ID || first.Sequence >= second.Sequence {
		t.Fatalf("message ordering/idempotency = first %+v duplicate %+v second %+v", first, duplicate, second)
	}
	if first.RecipientRole != "planner" || first.AgentSessionID == "" || first.AgentSessionID != second.AgentSessionID {
		t.Fatalf("routing = first %+v second %+v", first, second)
	}
	unchanged, err := db.Task(ctx, task.ID)
	if err != nil || unchanged.State != string(Draft) {
		t.Fatalf("message executed control: task = %+v err = %v", unchanged, err)
	}
	if err = service.Abort(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := db.Messages(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.DeliveryStatus != "failed" || message.FailureReason != "task_aborted" {
			t.Fatalf("aborted message = %+v", message)
		}
	}
	if _, err = service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "abort this task", IdempotencyKey: "three"}); err == nil {
		t.Fatal("message accepted after abort")
	}
}

func TestDrainMessagesResumesSameSessionWithExactText(t *testing.T) {
	adapter := &scriptedHarness{results: []harness.Result{{
		Text:         `{"status":"success","summary":"planned","artifacts":[],"notes_for_next_agent":"","steps":[{"id":"one","description":"change","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`,
		SessionReady: true, AccountingComplete: true,
	}}}
	service, db, task := messageTestService(t, adapter)
	ctx := context.Background()
	target := store.Phase{ID: "target-plan", TaskID: task.ID, Sequence: 1, Name: "planning", Kind: "agent", Owner: "planner", Status: "success", Attempt: 1}
	if err := db.AddPhase(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCheck(ctx, store.Check{ID: "test", TaskID: task.ID, PhaseID: "checks", Name: "test", Command: "go test", Attempt: 1, Status: "failed", Output: "failure evidence"}); err != nil {
		t.Fatal(err)
	}
	message, err := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "Keep the public API.", Target: MessageTarget{AttemptID: target.ID}, IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	phase := store.Phase{ID: "planning-message", TaskID: task.ID, Name: "planning", Kind: "agent", Owner: "planner"}
	if _, err = service.drainMessages(ctx, task, phase, "planner", validatorForRole("planner")); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 1 || !adapter.requests[0].Resume || adapter.requests[0].Prompt != message.Text || adapter.requests[0].SessionID != message.AgentSessionID {
		t.Fatalf("continuation request = %+v", adapter.requests)
	}
	if !strings.Contains(adapter.requests[0].SystemPrompt, `"attempt_id":"target-plan"`) || !strings.Contains(adapter.requests[0].SystemPrompt, `"failed_checks"`) || !strings.Contains(adapter.requests[0].SystemPrompt, "failure evidence") {
		t.Fatalf("continuation context = %q", adapter.requests[0].SystemPrompt)
	}
	messages, err := db.Messages(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].DeliveryStatus != "delivered" || messages[0].DeliveredAt == "" {
		t.Fatalf("delivered message = %+v", messages[0])
	}
}

func TestTerminalHarnessFailureFailsMessageAndEmitsEvent(t *testing.T) {
	adapter := &errorScriptedHarness{results: []harness.Result{{SessionReady: true, AccountingComplete: true}}, errs: []error{errors.New("terminal harness failure")}}
	service, db, task := messageTestService(t, adapter)
	ctx := context.Background()
	message, err := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "Continue.", IdempotencyKey: "failure"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.drainMessages(ctx, task, store.Phase{ID: "phase", TaskID: task.ID, Name: "planning", Kind: "agent", Owner: "planner"}, "planner", validatorForRole("planner"))
	if err == nil {
		t.Fatal("expected harness failure")
	}
	stored, err := db.MessageByIdempotencyKey(ctx, task.ID, message.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeliveryStatus != "failed" || stored.FailureReason != "harness_error" || stored.FailedAt == "" {
		t.Fatalf("failed message = %+v", stored)
	}
	events, err := db.Events(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if payload, ok := event.Payload.(map[string]any); ok && payload["message_id"] == message.ID && payload["delivery_status"] == "failed" && payload["failure_reason"] == "harness_error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed lifecycle event not found: %+v", events)
	}
}

func TestAgentCompletionSerializesFinalQueueCheckAndTransition(t *testing.T) {
	adapter := &scriptedHarness{results: []harness.Result{{Text: `{"status":"success","summary":"replanned","artifacts":[],"notes_for_next_agent":"","steps":[{"id":"one","description":"change","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`, SessionReady: true, AccountingComplete: true}}}
	service, db, task := messageTestService(t, adapter)
	ctx := context.Background()
	if err := service.ensureBranch(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	task, _ = db.Task(ctx, task.ID)
	phase := store.Phase{ID: "planning-boundary", TaskID: task.ID, Sequence: 1, Name: "planning", Kind: "agent", Owner: "planner", Status: "running", Attempt: 1, BranchID: task.SelectedBranchID}
	if err := db.AddPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update tasks set state='planning',active_phase=? where id=?`, phase.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = db.Task(ctx, task.ID)
	if _, err := service.ensureAgentSession(ctx, task, "planner"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		_, completeErr := service.completeAgentPhase(ctx, task, phase, "planner", validatorForRole("planner"), `{"status":"success"}`, func(string) error {
			close(entered)
			<-release
			return nil
		}, AwaitingApproval)
		completed <- completeErr
	}()
	<-entered
	sent := make(chan error, 1)
	go func() {
		_, sendErr := service.SendMessage(ctx, task.ID, "tester", SendMessageRequest{Text: "Change plan.", IdempotencyKey: "boundary"})
		sent <- sendErr
	}()
	select {
	case err := <-sent:
		t.Fatalf("send crossed locked completion boundary: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages, listErr := db.Messages(ctx, task.ID)
		current, taskErr := db.Task(ctx, task.ID)
		if listErr == nil && taskErr == nil && len(messages) == 1 && messages[0].DeliveryStatus == "delivered" && current.State == string(AwaitingApproval) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("boundary message was not delivered before workflow settled")
}

func TestExactRetryIsIdempotentAndUsesOriginalInput(t *testing.T) {
	adapter := &scriptedHarness{results: []harness.Result{{Text: `{"status":"success","summary":"planned","artifacts":[],"notes_for_next_agent":"","steps":[{"id":"one","description":"change","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`, SessionReady: true, AccountingComplete: true}}}
	service, db, task := messageTestService(t, adapter)
	ctx := context.Background()
	if err := service.ensureBranch(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	task, _ = db.Task(ctx, task.ID)
	definitionID := service.ensureDefinition(ctx, task.ID, "planning", "agent", "planner")
	phase := store.Phase{ID: randomID(), TaskID: task.ID, Sequence: 1, Name: "planning", Kind: "agent", Owner: "planner", Description: "Plan", Status: "failed", Attempt: 1, BranchID: task.SelectedBranchID, DefinitionID: definitionID, InputSnapshot: "input-snapshot"}
	if err := db.AddPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(ctx, store.WorkspaceSnapshot{Digest: phase.InputSnapshot, TaskID: task.ID, Path: filepath.Join(task.WorkspacePath, "workspace", "repositories"), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update tasks set state='blocked' where id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	first, err := service.Retry(ctx, task.ID, phase.ID, RetryRequest{IdempotencyKey: "retry-one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Retry(ctx, task.ID, phase.ID, RetryRequest{IdempotencyKey: "retry-one"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("retry results differ: %+v %+v", first, second)
	}
	var retried store.Phase
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		retried, err = db.PhaseByID(ctx, task.ID, first.AttemptID)
		current, taskErr := db.Task(ctx, task.ID)
		if err == nil && taskErr == nil && retried.Status == "success" && current.State == string(AwaitingApproval) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if retried.InputSnapshot != phase.InputSnapshot || retried.DefinitionID != phase.DefinitionID || retried.Status != "success" {
		t.Fatalf("retry attempt = %+v", retried)
	}
	branches, err := db.Branches(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want initial plus one child", len(branches))
	}
}

func TestAvailableActionsContainControlsOnly(t *testing.T) {
	for _, actions := range [][]string{
		AvailableActions(nil, string(Draft)),
		AvailableActions(&store.Phase{Status: "running", Kind: "agent"}, string(Building)),
		AvailableActions(&store.Phase{Status: "failed", Kind: "check"}, string(Blocked)),
	} {
		for _, action := range actions {
			switch action {
			case "start", "approve", "pause", "resume", "abort", "retry":
			default:
				t.Fatalf("non-control action %q in %v", action, actions)
			}
		}
	}
}

package stagekit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func budgetTestDB(t *testing.T) (*store.Store, *Kit, store.Task) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kit := New(db, nil, nil, config.Config{}, "", filepath.Join(root, "tasks"))

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	task := store.Task{
		ID: "SF-1", Request: "fix", WorkspacePath: filepath.Join(root, "tasks", "SF-1"),
		RepositoryType: "github", RepositorySource: "owner/repository", State: string(Blocked),
		CreatedAt: createdAt, StartedAt: createdAt,
	}
	if err = db.Tasks.CreateActive(ctx, task); err != nil {
		t.Fatal(err)
	}
	return db, kit, task
}

func addFailedPhase(t *testing.T, db *store.Store, taskID, id string, sequence int) {
	t.Helper()
	if err := db.Phases.Add(context.Background(), store.Phase{
		ID: id, TaskID: taskID, Sequence: sequence, Name: "review", Kind: "review",
		Owner: "reviewer", Status: "failed", Attempt: 1, Error: "boom",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnforceStageBudgetAllowsAttemptsUnderThreshold(t *testing.T) {
	db, kit, task := budgetTestDB(t)
	ctx := context.Background()
	addFailedPhase(t, db, task.ID, "phase-1", 1)
	addFailedPhase(t, db, task.ID, "phase-2", 2)

	if err := kit.EnforceStageBudget(ctx, task.ID, "review", "reviewer"); err != nil {
		t.Fatalf("budget enforced below threshold: %v", err)
	}
}

func TestEnforceStageBudgetRefusesRetryAndFailsQueuedMessages(t *testing.T) {
	db, kit, task := budgetTestDB(t)
	ctx := context.Background()
	for index, id := range []string{"phase-1", "phase-2", "phase-3"} {
		addFailedPhase(t, db, task.ID, id, index+1)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, _, err := db.Messages.Save(ctx, store.Message{
		ID: "message-1", TaskID: task.ID, Actor: "owner", Text: "try again",
		IdempotencyKey: "key-1", StageID: "review", RecipientRole: "reviewer",
		AgentSessionID: "session-1", DeliveryStatus: "queued", CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}

	err := kit.EnforceStageBudget(ctx, task.ID, "review", "reviewer")
	if !errors.Is(err, ErrStageBudgetExceeded) {
		t.Fatalf("error = %v, want %v", err, ErrStageBudgetExceeded)
	}
	queued, err := db.Messages.QueuedForStages(ctx, task.ID, "review", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("queued stage message survived budget exhaustion")
	}
	messages, err := db.Messages.List(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].DeliveryStatus != "failed" || messages[0].FailureReason != "stage_retry_budget_exceeded" {
		t.Fatalf("message = %+v, want one failed with budget reason", messages)
	}
}

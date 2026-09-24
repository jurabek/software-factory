package stagekit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestBeginOrReusePhaseStartsQueuedAttempt(t *testing.T) {
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
		RepositoryType: "github", RepositorySource: "owner/repository", State: Blocked,
		CreatedAt: createdAt, StartedAt: createdAt,
	}
	if err = db.Tasks.CreateActive(ctx, task); err != nil {
		t.Fatal(err)
	}
	queued := store.Phase{
		ID: "phase-2", TaskID: task.ID, Sequence: 2, Name: "building", Kind: "agent", Owner: "builder",
		Status: "queued", Attempt: 2, DefinitionID: "definition-1", InputSnapshot: "in-snap",
	}
	if err = db.Phases.Add(ctx, queued); err != nil {
		t.Fatal(err)
	}

	phase, err := kit.BeginOrReusePhase(ctx, task.ID, "building", "agent", "builder", "Execute building")
	if err != nil {
		t.Fatal(err)
	}
	if phase.ID != queued.ID {
		t.Fatalf("reused phase = %s, want queued attempt %s", phase.ID, queued.ID)
	}
	if phase.InputSnapshot != "in-snap" || phase.DefinitionID != "definition-1" {
		t.Fatalf("queued attempt identity lost: %#v", phase)
	}
	stored, err := db.Phases.ByID(ctx, task.ID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" {
		t.Fatalf("queued attempt status = %q, want running", stored.Status)
	}
}

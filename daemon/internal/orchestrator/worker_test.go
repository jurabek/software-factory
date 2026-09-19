package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestExecutionOwnerAdmitsOneSuccessorAfterCurrentSettlement(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	created := store.Task{ID: "task-1", Request: "change", WorkspacePath: filepath.Join(root, "tasks", "task-1"), State: string(Preparing), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = db.CreateTask(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	service := New(root, Dependencies{Store: db})
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

func TestHandleEventsStopsWhenContextIsCanceled(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := New(root, Dependencies{Store: db})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.HandleEvents(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event handler did not stop after context cancellation")
	}
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

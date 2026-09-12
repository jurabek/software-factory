package factory

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

type fakeSandbox struct {
	requests   []MaterializationRequest
	cleanup    []CleanupRequest
	failAt     int
	cleanupErr error
}

func (f *fakeSandbox) Materialize(_ context.Context, request MaterializationRequest) (Materialization, error) {
	f.requests = append(f.requests, request)
	if f.failAt > 0 && len(f.requests) == f.failAt {
		return Materialization{}, errors.New("materialization failure")
	}
	return Materialization{Root: "/canonical/repository", BaseSHA: "base", BranchName: request.TaskID, Checks: []Check{{ID: "check", Command: "true"}}}, nil
}

func (f *fakeSandbox) Cleanup(_ context.Context, request CleanupRequest) error {
	f.cleanup = append(f.cleanup, request)
	return f.cleanupErr
}

func TestPrepareRepositoryUsesTaskIdentity(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &fakeSandbox{}
	service := NewService(root, Dependencies{Store: db, Sandbox: fake})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "prepare", Repository: Repository{Type: "github", Repo: "owner/one"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.prepareRepository(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 || fake.requests[0].TaskID != task.ID || fake.requests[0].Destination != filepath.Join(task.WorkspacePath, "workspace", "repository") {
		t.Fatalf("requests = %#v", fake.requests)
	}
}

func TestDeleteDelegatesCleanupWithoutBlockingDeletion(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &fakeSandbox{cleanupErr: errors.New("cleanup failed")}
	service := NewService(root, Dependencies{Store: db, Sandbox: fake})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "delete", Repository: Repository{Type: "github", Repo: "owner/app"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Transition(context.Background(), task.ID, task.State, string(Blocked), "", ""); err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.cleanup) != 1 || fake.cleanup[0].TaskID != task.ID || fake.cleanup[0].WorkspaceRoot != task.WorkspacePath {
		t.Fatalf("cleanup requests = %#v", fake.cleanup)
	}
	if _, err = db.Task(context.Background(), task.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("task lookup error = %v, want not found", err)
	}
}

func TestPrepareRepositoryReturnsSandboxFailure(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &fakeSandbox{failAt: 1}
	service := NewService(root, Dependencies{Store: db, Sandbox: fake})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "prepare", Repository: Repository{Type: "github", Repo: "owner/one"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.prepareRepository(context.Background(), task); err == nil {
		t.Fatal("expected sandbox failure")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.requests))
	}
}

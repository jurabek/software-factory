package stagekit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type fakeSandbox struct {
	requests   []workspace.MaterializationRequest
	cleanup    []workspace.CleanupRequest
	failAt     int
	cleanupErr error
}

func (f *fakeSandbox) Materialize(_ context.Context, request workspace.MaterializationRequest) (workspace.Materialization, error) {
	f.requests = append(f.requests, request)
	if f.failAt > 0 && len(f.requests) == f.failAt {
		return workspace.Materialization{}, errors.New("materialization failure")
	}
	return workspace.Materialization{Root: "/canonical/repository", BaseSHA: "base", BranchName: request.TaskID, Checks: []workspace.Check{{ID: "check", Command: "true"}}}, nil
}

func (f *fakeSandbox) Cleanup(_ context.Context, request workspace.CleanupRequest) error {
	f.cleanup = append(f.cleanup, request)
	return f.cleanupErr
}

func prepareTestKit(t *testing.T, sandbox workspace.Sandbox) (*Kit, store.Task) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	task := store.Task{
		ID: "task-1", Request: "prepare", WorkspacePath: filepath.Join(root, "tasks", "task-1"),
		RepositoryType: "github", RepositorySource: "owner/one", State: "preparing",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err = os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = db.Tasks.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return New(db, nil, sandbox, config.Config{}, "", root), task
}

func TestPrepareRepositoryUsesTaskIdentity(t *testing.T) {
	fake := &fakeSandbox{}
	kit, task := prepareTestKit(t, fake)
	if _, err := kit.EnsurePrepared(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 || fake.requests[0].TaskID != task.ID || fake.requests[0].Destination != filepath.Join(task.WorkspacePath, "workspace", "repository") {
		t.Fatalf("requests = %#v", fake.requests)
	}
}

func TestPrepareRepositoryReturnsSandboxFailure(t *testing.T) {
	fake := &fakeSandbox{failAt: 1}
	kit, task := prepareTestKit(t, fake)
	if _, err := kit.EnsurePrepared(context.Background(), task); err == nil {
		t.Fatal("expected sandbox failure")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.requests))
	}
}

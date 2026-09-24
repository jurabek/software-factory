package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func testTaskService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	configured := config.Config{Agents: []config.Agent{{Name: "planner"}, {Name: "builder"}, {Name: "reviewer"}}}
	return New(root, Deps{Store: db, Config: configured}), db, root
}

func TestCreateAllocatesWorkspaceAndConfigSnapshot(t *testing.T) {
	service, db, root := testTaskService(t)
	created, err := service.Create(context.Background(), CreateRequest{Request: " build ", Repository: Repository{Type: "github", Repo: "owner/repository"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Request != "build" || created.WorkspacePath != filepath.Join(root, "tasks", created.ID) {
		t.Fatalf("created task = %+v", created)
	}
	for _, relative := range []string{"task.json", "workspace/repository", "attempts", "snapshots", "sessions"} {
		if _, err = os.Stat(filepath.Join(created.WorkspacePath, relative)); err != nil {
			t.Fatalf("workspace item %s: %v", relative, err)
		}
	}
	stored, err := db.Tasks.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigSnapshot == "" {
		t.Fatalf("task missing config snapshot: %+v", stored)
	}
	configured, _, err := config.Parse([]byte(stored.ConfigSnapshot), root)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, ok := configured.DefaultPipeline()
	if !ok || len(pipeline.Stages) != 4 {
		t.Fatalf("frozen pipeline = %+v", pipeline)
	}
}

func TestCreateSessionUsesRootTaskAndDeleteCleansAllWorkspaces(t *testing.T) {
	service, db, _ := testTaskService(t)
	ctx := context.Background()
	rootTask, err := service.Create(ctx, CreateRequest{Request: "root", Repository: Repository{Type: "github", Repo: "owner/repository"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `update tasks set state='completed' where id=?`, rootTask.ID); err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateSession(ctx, rootTask.ID, CreateSessionRequest{Request: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `update tasks set state='completed' where id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateSession(ctx, first.ID, CreateSessionRequest{Request: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentTaskID != rootTask.ID || second.RepositorySource != rootTask.RepositorySource || second.ConfigSnapshot != rootTask.ConfigSnapshot {
		t.Fatalf("session did not inherit root task: %+v", second)
	}
	for _, taskID := range []string{rootTask.ID, first.ID, second.ID} {
		if _, err = db.ExecContext(ctx, `update tasks set state='completed' where id=?`, taskID); err != nil {
			t.Fatal(err)
		}
	}
	if err = service.Delete(ctx, rootTask.ID); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{rootTask.WorkspacePath, first.WorkspacePath, second.WorkspacePath} {
		if _, err = os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatalf("workspace %q still exists: %v", workspace, err)
		}
	}
	if _, err = db.Tasks.Get(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("child lookup error = %v, want not found", err)
	}
}

func TestNormalizeRepositoryRejectsInvalidSources(t *testing.T) {
	for _, repository := range []Repository{{Type: "local", Path: "relative"}, {Type: "github", Repo: "owner"}, {Type: "unknown"}} {
		if _, _, err := normalizeRepository(repository); err == nil {
			t.Fatalf("repository accepted: %+v", repository)
		}
	}
}

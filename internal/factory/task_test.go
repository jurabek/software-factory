package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/internal/config"
	"github.com/jurabek/software-factory/internal/store"
)

func TestCreateTaskAllocatesWorkspaceForMultipleRepositories(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	task, err := service.Create(context.Background(), CreateRequest{Request: "Coordinate API and UI", Repositories: []Repository{{Name: "api", Type: "local", Path: filepath.Join(root, "api"), Primary: true}, {Name: "web", Type: "github", Repo: "owner/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(task.Repositories))
	}
	if task.WorkspacePath != filepath.Join(root, "tasks", task.ID) {
		t.Fatalf("workspace = %q", task.WorkspacePath)
	}
	for _, relative := range []string{"task.json", "workspace/repositories", "attempts", "snapshots", "artifacts", "sessions"} {
		if _, err = os.Stat(filepath.Join(task.WorkspacePath, relative)); err != nil {
			t.Fatalf("workspace item %s: %v", relative, err)
		}
	}
	stored, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Repositories) != 2 || !stored.Repositories[0].Primary {
		t.Fatalf("stored repositories = %#v", stored.Repositories)
	}
	listed, err := db.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || len(listed[0].Repositories) != 2 {
		t.Fatalf("listed tasks = %#v", listed)
	}
}

func TestCreateTaskRequiresOnePrimaryRepository(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	_, err = service.Create(context.Background(), CreateRequest{Request: "change", Repositories: []Repository{{Name: "one", Type: "github", Repo: "owner/one", Primary: true}, {Name: "two", Type: "github", Repo: "owner/two", Primary: true}}})
	if err == nil {
		t.Fatal("expected primary repository validation error")
	}
}

func TestCommentIsIdempotent(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	task, err := service.Create(context.Background(), CreateRequest{Request: "change", Repositories: []Repository{{Type: "github", Repo: "owner/repository"}}})
	if err != nil {
		t.Fatal(err)
	}
	request := InterventionRequest{TargetType: "task", TargetID: task.ID, Message: "Keep the public interface", IdempotencyKey: "comment-1"}
	first, err := service.Comment(context.Background(), task.ID, "tester", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Comment(context.Background(), task.ID, "tester", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("intervention IDs differ: %s %s", first.ID, second.ID)
	}
	values, err := db.Interventions(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("interventions = %d, want 1", len(values))
	}
}

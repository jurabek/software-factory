package factory

import (
	"context"
	"errors"
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

func TestCreateSessionInheritsTaskConfiguration(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	task, err := service.Create(context.Background(), CreateRequest{
		Request:      "Add task sessions",
		Repositories: []Repository{{Name: "app", Type: "github", Repo: "owner/app", Primary: true}},
		CodingAgent:  "pi",
		Model:        "provider/model",
		Thinking:     "high",
	})
	if err != nil {
		t.Fatal(err)
	}

	session, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "Review the API"})
	if err != nil {
		t.Fatal(err)
	}
	if session.ParentTaskID != task.ID {
		t.Fatalf("parent task = %q, want %q", session.ParentTaskID, task.ID)
	}
	if session.Request != "Review the API" || session.State != string(Draft) {
		t.Fatalf("session = %#v", session)
	}
	if session.CodingAgent != task.CodingAgent || session.Model != task.Model || session.Thinking != task.Thinking {
		t.Fatalf("session agent configuration = %#v, task = %#v", session, task)
	}
	if len(session.Repositories) != 1 || session.Repositories[0].SourceValue != "owner/app" || session.Repositories[0].TaskID != session.ID {
		t.Fatalf("session repositories = %#v", session.Repositories)
	}

	sessions, err := db.TaskSessions(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != task.ID || sessions[1].ID != session.ID {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestCreateSessionUsesRootForNestedSessionRequest(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	task, err := service.Create(context.Background(), CreateRequest{Request: "Task", Repositories: []Repository{{Type: "github", Repo: "owner/app"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "First session"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateSession(context.Background(), first.ID, CreateSessionRequest{Request: "Second session"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentTaskID != task.ID {
		t.Fatalf("parent task = %q, want root %q", second.ParentTaskID, task.ID)
	}
}

func TestDeleteRootTaskDeletesAllSessionWorkspaces(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, db, config.Config{}, "", nil, nil)
	task, err := service.Create(context.Background(), CreateRequest{Request: "Task", Repositories: []Repository{{Type: "github", Repo: "owner/app"}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "Session"})
	if err != nil {
		t.Fatal(err)
	}

	if err = service.Delete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{task.WorkspacePath, session.WorkspacePath} {
		if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
			t.Fatalf("workspace %q still exists: %v", workspace, statErr)
		}
	}
	if _, err = db.Task(context.Background(), session.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("child session lookup error = %v, want not found", err)
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

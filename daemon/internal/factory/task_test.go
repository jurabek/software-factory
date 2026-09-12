package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestCreateTaskAllocatesSingletonRepositoryWorkspace(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, Dependencies{Store: db})
	task, err := service.Create(context.Background(), CreateRequest{Request: "Coordinate API and UI", Repository: Repository{Type: "github", Repo: "owner/api"}})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
	if task.RepositoryType != "github" || task.RepositorySource != "owner/api" {
		t.Fatalf("repository = %#v", task)
	}
	if task.WorkspacePath != filepath.Join(root, "tasks", task.ID) {
		t.Fatalf("workspace = %q", task.WorkspacePath)
	}
	for _, relative := range []string{"task.json", "workspace/repository", "attempts", "snapshots", "artifacts", "sessions"} {
		if _, err = os.Stat(filepath.Join(task.WorkspacePath, relative)); err != nil {
			t.Fatalf("workspace item %s: %v", relative, err)
		}
	}
	stored, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RepositoryType != "github" || stored.RepositorySource != "owner/api" {
		t.Fatalf("stored task = %#v", stored)
	}
	listed, err := db.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].RepositorySource != "owner/api" {
		t.Fatalf("listed tasks = %#v", listed)
	}
}

func TestCreateTaskRequiresRepository(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, Dependencies{Store: db})
	_, err = service.Create(context.Background(), CreateRequest{Request: "change"})
	if err == nil {
		t.Fatal("expected repository validation error")
	}
}

func TestCreateSessionInheritsTaskConfiguration(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(root, Dependencies{Store: db})
	task, err := service.Create(context.Background(), CreateRequest{
		Request:     "Add task sessions",
		Repository:  Repository{Type: "github", Repo: "owner/app"},
		CodingAgent: "pi",
		Model:       "provider/model",
		Thinking:    "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())

	session, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "Review the API"})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
	if session.ParentTaskID != task.ID {
		t.Fatalf("parent task = %q, want %q", session.ParentTaskID, task.ID)
	}
	if session.Request != "Review the API" || session.State != string(Preparing) {
		t.Fatalf("session = %#v", session)
	}
	if session.CodingAgent != task.CodingAgent || session.Model != task.Model || session.Thinking != task.Thinking {
		t.Fatalf("session agent configuration = %#v, task = %#v", session, task)
	}
	if session.RepositorySource != "owner/app" || session.RepositoryType != "github" {
		t.Fatalf("session repository = %#v", session)
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
	service := NewService(root, Dependencies{Store: db})
	task, err := service.Create(context.Background(), CreateRequest{Request: "Task", Repository: Repository{Type: "github", Repo: "owner/app"}})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
	first, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "First session"})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
	second, err := service.CreateSession(context.Background(), first.ID, CreateSessionRequest{Request: "Second session"})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
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
	service := NewService(root, Dependencies{Store: db})
	task, err := service.Create(context.Background(), CreateRequest{Request: "Task", Repository: Repository{Type: "github", Repo: "owner/app"}})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())
	session, err := service.CreateSession(context.Background(), task.ID, CreateSessionRequest{Request: "Session"})
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown(context.Background())

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

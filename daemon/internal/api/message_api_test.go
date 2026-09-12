package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestLegacyWritesAreRemovedAndNewWritesRejectOrchestrationFields(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		body string
		want int
	}{
		{path: "/api/v1/tasks/task/interventions", body: `{}`, want: http.StatusMethodNotAllowed},
		{path: "/api/v1/tasks/task/feedback", body: `{}`, want: http.StatusNotFound},
		{path: "/api/v1/tasks/task/messages", body: `{"text":"change","idempotency_key":"one","intent":"repair"}`, want: http.StatusUnprocessableEntity},
		{path: "/api/v1/tasks/task/attempts/attempt/retry", body: `{"idempotency_key":"one","text":"change it"}`, want: http.StatusUnprocessableEntity},
		{path: "/api/v1/tasks/task/pause", body: `{"text":"pause"}`, want: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
		authorize(request)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("POST %s status = %d, want %d: %s", test.path, response.Code, test.want, response.Body.String())
		}
	}
}

func TestTaskReadsExposeAuthoritativeAvailableActions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	task := store.Task{ID: "task-actions", Request: "request", WorkspacePath: t.TempDir(), RepositoryType: "github", RepositorySource: "owner/repo", State: "preparing", CreatedAt: createdAt}
	if err = db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/tasks/task-actions", "/api/v1/tasks"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		authorize(request)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"available_actions":["pause","abort"]`)) {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	phase := store.Phase{ID: "done", TaskID: task.ID, Sequence: 1, Name: "reviewing", Kind: "agent", Owner: "reviewer", Status: "success", Attempt: 1}
	if err = db.AddPhase(context.Background(), phase); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), `update tasks set state='completed',active_phase=null where id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-actions", nil)
	authorize(request)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"available_actions":["retry"]`)) {
		t.Fatalf("completed task = %d %s", response.Code, response.Body.String())
	}
}

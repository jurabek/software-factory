package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestEventsTailReturnsNewestEventsInSequenceOrder(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	task := store.Task{ID: "task-1", Request: "request", WorkspacePath: t.TempDir(), State: "draft", CreatedAt: createdAt, Repositories: []store.TaskRepository{{ID: "repository-1", TaskID: "task-1", Name: "source", SourceType: "local", SourceValue: t.TempDir(), Primary: true, CreatedAt: createdAt}}}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	taskDir := t.TempDir()
	for index := 1; index <= 3; index++ {
		_, err := db.AppendEvent(context.Background(), taskDir, store.Event{
			ID:        fmt.Sprintf("event-%d", index),
			TaskID:    task.ID,
			Type:      "log",
			Name:      fmt.Sprintf("event %d", index),
			Payload:   map[string]any{"index": index},
			StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, RemoteAccess{DaemonID: "daemon-1"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/events?tail=2", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Events []store.Event `json:"events"`
		Cursor int64         `json:"cursor"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 || body.Events[0].ID != "event-2" || body.Events[1].ID != "event-3" {
		t.Fatalf("events = %#v", body.Events)
	}
	if body.Cursor != body.Events[1].Sequence {
		t.Fatalf("cursor = %d, want %d", body.Cursor, body.Events[1].Sequence)
	}
}

func TestEmptyCollectionsAreJSONArrays(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, RemoteAccess{DaemonID: "daemon-1"})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ path, want string }{
		{path: "/api/v1/tasks", want: "[]\n"},
		{path: "/api/v1/health", want: "{\"errors\":[],\"status\":\"ok\"}\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if response.Body.String() != test.want {
				t.Fatalf("body = %s, want %s", response.Body.String(), test.want)
			}
		})
	}
}

func TestCreateTaskAcceptsMultipleRepositories(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := factory.NewService(root, db, config.Config{}, "", nil, nil)
	server, err := New(db, service, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, RemoteAccess{DaemonID: "daemon-1"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"request":"Coordinate changes","repositories":[{"name":"api","type":"github","repo":"owner/api","primary":true},{"name":"web","type":"github","repo":"owner/web"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(body))
	request.Header.Set("X-Software-Factory-Token", server.token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var task store.Task
	if err = json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if len(task.Repositories) != 2 || task.WorkspacePath == "" {
		t.Fatalf("task = %#v", task)
	}
}

func TestCreateAndListTaskSessions(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := factory.NewService(root, db, config.Config{}, "", nil, nil)
	server, err := New(db, service, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, RemoteAccess{DaemonID: "daemon-1"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.Create(context.Background(), factory.CreateRequest{Request: "Parent task", Repositories: []factory.Repository{{Type: "github", Repo: "owner/app"}}})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/sessions", bytes.NewBufferString(`{"request":"Investigate another approach"}`))
	request.Header.Set("X-Software-Factory-Token", server.token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var created store.Task
	if err = json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ParentTaskID != task.ID {
		t.Fatalf("parent task = %q, want %q", created.ParentTaskID, task.ID)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+task.ID+"/sessions", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var sessions []store.Task
	if err = json.NewDecoder(response.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[1].ID != created.ID {
		t.Fatalf("sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/sessions", bytes.NewBufferString(`{"request":" "}`))
	request.Header.Set("X-Software-Factory-Token", server.token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty request status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestLegacyRouteHasNoAlias(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, RemoteAccess{DaemonID: "daemon-1"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRemoteAccessRequiresDaemonCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const credential = "remote-test-credential-with-32-characters"
	server, err := New(db, nil, config.Config{}, nil, nil, nil, nil, RemoteAccess{DaemonID: "daemon-identity", Token: credential})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name          string
		path          string
		authorization string
		wantStatus    int
	}{
		{name: "missing", path: "/api/v1/tasks", wantStatus: http.StatusUnauthorized},
		{name: "missing stream credential", path: "/api/v1/tasks/task-1/events/stream", wantStatus: http.StatusUnauthorized},
		{name: "incorrect", path: "/api/v1/tasks", authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "read", path: "/api/v1/tasks", authorization: "Bearer " + credential, wantStatus: http.StatusOK},
		{name: "identity", path: "/api/v1/identity", authorization: "Bearer " + credential, wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.RemoteAddr = "198.51.100.9:4000"
			request.Header.Set("Authorization", test.authorization)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/identity", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("loopback identity status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("loopback tasks status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/control", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("Authorization", "Bearer "+credential)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote-mode control status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestRemoteCredentialAuthorizesMutationWithoutLocalToken(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := factory.NewService(root, db, config.Config{}, "", nil, nil)
	const credential = "remote-test-credential-with-32-characters"
	server, err := New(db, service, config.Config{}, nil, nil, nil, nil, RemoteAccess{DaemonID: "daemon-identity", Token: credential})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{"request":"Remote task","repositories":[{"type":"github","repo":"owner/app"}]}`))
	request.RemoteAddr = "198.51.100.9:4000"
	request.Header.Set("Authorization", "Bearer "+credential)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var task store.Task
	if err = json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+task.ID+"/events/stream", nil).WithContext(ctx)
	request.RemoteAddr = "198.51.100.9:4000"
	request.Header.Set("Authorization", "Bearer "+credential)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream status, content type = %d, %q", response.Code, response.Header().Get("Content-Type"))
	}
}

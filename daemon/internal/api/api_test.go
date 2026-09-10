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
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestAccess() Access {
	return Access{DaemonID: "0123456789abcdef0123456789abcdef", Token: testToken}
}

func authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+testToken)
}

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
			Kind:      session.KindCustom,
			Name:      fmt.Sprintf("event %d", index),
			Payload:   session.CustomPayload{CustomType: "log", Data: session.BoundedJSON(map[string]any{"index": index})},
			Display:   session.NewCustom(session.CustomPayload{CustomType: "log", Data: session.BoundedJSON(map[string]any{"index": index})}).Display,
			StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/events?tail=2", nil)
	authorize(request)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Events        []store.Event `json:"events"`
		Cursor        int64         `json:"cursor"`
		FormatVersion int           `json:"format_version"`
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
	if body.FormatVersion != session.FormatVersion {
		t.Fatalf("format_version = %d, want %d", body.FormatVersion, session.FormatVersion)
	}
}

func TestEmptyCollectionsAreJSONArrays(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ path, want string }{
		{path: "/api/v1/tasks", want: "[]\n"},
		{path: "/api/v1/health", want: "{\"errors\":[],\"status\":\"ok\"}\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			authorize(request)
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
	service := factory.NewService(root, factory.Dependencies{Store: db})
	server, err := New(db, service, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"request":"Coordinate changes","repositories":[{"name":"api","type":"github","repo":"owner/api","primary":true},{"name":"web","type":"github","repo":"owner/web"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(body))
	authorize(request)
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
	service := factory.NewService(root, factory.Dependencies{Store: db})
	server, err := New(db, service, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.Create(context.Background(), factory.CreateRequest{Request: "Parent task", Repositories: []factory.Repository{{Type: "github", Repo: "owner/app"}}})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/sessions", bytes.NewBufferString(`{"request":"Investigate another approach"}`))
	authorize(request)
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
	authorize(request)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var sessions []store.TaskSession
	if err = json.NewDecoder(response.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[1].ID != created.ID {
		t.Fatalf("sessions = %#v", sessions)
	}
	for _, value := range sessions {
		if value.AgentSessions == nil {
			t.Fatalf("agent_sessions is null for session %s, want []", value.ID)
		}
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/sessions", bytes.NewBufferString(`{"request":" "}`))
	authorize(request)
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
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns", nil)
	authorize(request)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestTokenRequiredForAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, nil, func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }, newTestAccess())
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
		{name: "wrong", path: "/api/v1/tasks", authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "no prefix", path: "/api/v1/tasks", authorization: testToken, wantStatus: http.StatusUnauthorized},
		{name: "valid", path: "/api/v1/tasks", authorization: "Bearer " + testToken, wantStatus: http.StatusOK},
		{name: "identity valid", path: "/api/v1/identity", authorization: "Bearer " + testToken, wantStatus: http.StatusOK},
		{name: "health public", path: "/api/v1/health", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestNewRequiresToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, nil, config.Config{}, nil, nil, nil, nil, Access{DaemonID: "x"}); err == nil {
		t.Fatal("New succeeded without token")
	}
}

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type server struct {
	db               *store.DB
	factory          *factory.Service
	config           config.Config
	validationErrors []string
	loadError        error
	harnesses        []string
	models           func(context.Context, string) ([]config.Model, error)
	token            string
	daemonID         string
}

type Access struct {
	DaemonID string
	Token    string
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type taskResponse struct {
	store.Task
	AvailableActions []string `json:"available_actions"`
}

type taskSessionResponse struct {
	store.Task
	AgentSessions    []store.AgentSession `json:"agent_sessions"`
	AvailableActions []string             `json:"available_actions"`
}

func New(db *store.DB, service *factory.Service, cfg config.Config, problems []string, loadErr error, harnesses []string, models func(context.Context, string) ([]config.Model, error), access Access) (http.Handler, error) {
	if access.Token == "" {
		return nil, errors.New("daemon token is required")
	}
	if harnesses == nil {
		harnesses = []string{"pi"}
	}
	if models == nil {
		models = func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }
	}
	s := &server{db: db, factory: service, config: cfg, validationErrors: problems, loadError: loadErr, harnesses: harnesses, models: models, token: access.Token, daemonID: access.DaemonID}
	return noStore(s.routes()), nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/identity", s.identity)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/config", s.configRead)
	mux.HandleFunc("GET /api/v1/harnesses", s.harnessesRead)
	mux.HandleFunc("GET /api/v1/models", s.modelsRead)
	mux.HandleFunc("GET /api/v1/pipelines", s.pipelinesRead)
	mux.HandleFunc("POST /api/v1/tasks", s.create)
	mux.HandleFunc("GET /api/v1/tasks", s.tasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.task)
	mux.HandleFunc("POST /api/v1/tasks/{id}/sessions", s.createSession)
	mux.HandleFunc("GET /api/v1/tasks/{id}/sessions", s.taskSessions)
	mux.HandleFunc("POST /api/v1/tasks/{id}/messages", s.sendMessage)
	mux.HandleFunc("GET /api/v1/tasks/{id}/messages", s.messages)
	mux.HandleFunc("POST /api/v1/tasks/{id}/attempts/{attemptID}/retry", s.retry)
	mux.Handle("POST /api/v1/tasks/{id}/start", s.control(func(ctx context.Context, id string) error { return s.factory.Start(ctx, id) }))
	mux.HandleFunc("POST /api/v1/tasks/{id}/approve", s.approve)
	mux.Handle("POST /api/v1/tasks/{id}/pause", s.control(func(ctx context.Context, id string) error { return s.factory.Pause(ctx, id) }))
	mux.Handle("POST /api/v1/tasks/{id}/resume", s.control(func(ctx context.Context, id string) error { return s.factory.Resume(ctx, id) }))
	mux.Handle("POST /api/v1/tasks/{id}/abort", s.control(func(ctx context.Context, id string) error { return s.factory.Abort(ctx, id) }))
	mux.HandleFunc("GET /api/v1/tasks/{id}/interventions", s.interventions)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.delete)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts", s.attempts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts/{attemptID}", s.attempt)
	mux.HandleFunc("GET /api/v1/tasks/{id}/branches", s.branches)
	mux.HandleFunc("GET /api/v1/tasks/{id}/artifacts", s.artifacts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events/stream", s.stream)
	mux.HandleFunc("GET /api/v1/tasks/{id}/results", s.results)
	mux.HandleFunc("GET /api/v1/tasks/{id}/checks", s.checks)
	mux.HandleFunc("GET /api/v1/tasks/{id}/diff", s.diff)
	return s.authenticate(mux)
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, prefix) {
			fail(w, http.StatusUnauthorized, "invalid_credential", "daemon token missing or invalid")
			return
		}
		provided := strings.TrimPrefix(authorization, prefix)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			fail(w, http.StatusUnauthorized, "invalid_credential", "daemon token missing or invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) identity(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]string{"id": s.daemonID})
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	problems := append([]string{}, s.validationErrors...)
	if s.loadError != nil {
		status = "degraded"
		problems = append(problems, s.loadError.Error())
	}
	if _, err := s.models(r.Context(), s.defaultHarness()); err != nil {
		status = "degraded"
		problems = append(problems, err.Error())
	}
	write(w, http.StatusOK, map[string]any{"status": status, "errors": problems})
}

func (s *server) configRead(w http.ResponseWriter, _ *http.Request) {
	problems := append([]string{}, s.validationErrors...)
	if s.loadError != nil {
		problems = append(problems, s.loadError.Error())
	}
	write(w, http.StatusOK, map[string]any{"config": s.config, "errors": problems})
}

func (s *server) harnessesRead(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{"harnesses": s.harnesses})
}

func (s *server) defaultHarness() string {
	if s.config.Defaults.CodingAgent != "" {
		return s.config.Defaults.CodingAgent
	}
	if len(s.harnesses) > 0 {
		return s.harnesses[0]
	}
	return "pi"
}

func (s *server) modelsRead(w http.ResponseWriter, r *http.Request) {
	harness := strings.TrimSpace(r.URL.Query().Get("harness"))
	if harness == "" {
		harness = s.defaultHarness()
	}
	known := false
	for _, name := range s.harnesses {
		if name == harness {
			known = true
			break
		}
	}
	if !known {
		fail(w, http.StatusUnprocessableEntity, "unknown_harness", "harness "+harness+" is not available")
		return
	}
	models, err := s.models(r.Context(), harness)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "models_unavailable", err.Error())
		return
	}
	write(w, http.StatusOK, map[string]any{"harness": harness, "models": models})
}

func (s *server) pipelinesRead(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, s.config.Pipelines)
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	request, err := decode[factory.CreateRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	task, err := s.factory.Create(r.Context(), request)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_task", err.Error())
		return
	}
	value, err := s.taskResponse(r.Context(), task)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusCreated, value)
}

func (s *server) tasks(w http.ResponseWriter, r *http.Request) {
	values, err := s.db.Tasks(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	response := make([]taskResponse, 0, len(values))
	for _, task := range values {
		value, viewErr := s.taskResponse(r.Context(), task)
		if viewErr != nil {
			internal(w, viewErr)
			return
		}
		response = append(response, value)
	}
	write(w, http.StatusOK, response)
}

func (s *server) task(w http.ResponseWriter, r *http.Request) {
	value, err := s.db.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	response, err := s.taskResponse(r.Context(), value)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, response)
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	request, err := decode[factory.CreateSessionRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Request) == "" {
		fail(w, http.StatusUnprocessableEntity, "invalid_session", "session description is required")
		return
	}
	session, err := s.factory.CreateSession(r.Context(), r.PathValue("id"), request)
	if err != nil {
		storeError(w, err)
		return
	}
	value, err := s.taskResponse(r.Context(), session)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusCreated, value)
}

func (s *server) taskResponse(ctx context.Context, task store.Task) (taskResponse, error) {
	phases, err := s.db.Phases(ctx, task.ID)
	if err != nil {
		return taskResponse{}, err
	}
	var phase *store.Phase
	if task.ActivePhase != "" {
		for index := range phases {
			if phases[index].ID == task.ActivePhase {
				phase = &phases[index]
				break
			}
		}
	}
	if phase == nil && len(phases) > 0 {
		phase = &phases[len(phases)-1]
	}
	if s.factory != nil {
		stages, projectionErr := s.factory.StageProjection(ctx, task)
		if projectionErr != nil {
			return taskResponse{}, projectionErr
		}
		task.Stages = stages
	}
	return taskResponse{Task: task, AvailableActions: factory.AvailableActions(phase, task.State)}, nil
}

func (s *server) taskSessions(w http.ResponseWriter, r *http.Request) {
	values, err := s.db.TaskSessionsWithAgents(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	response := make([]taskSessionResponse, 0, len(values))
	for _, session := range values {
		view, viewErr := s.taskResponse(r.Context(), session.Task)
		if viewErr != nil {
			internal(w, viewErr)
			return
		}
		response = append(response, taskSessionResponse{Task: view.Task, AgentSessions: session.AgentSessions, AvailableActions: view.AvailableActions})
	}
	write(w, http.StatusOK, response)
}

func (s *server) control(action func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) || !emptyBody(w, r) {
			return
		}
		if err := action(r.Context(), r.PathValue("id")); err != nil {
			storeError(w, err)
			return
		}
		write(w, http.StatusAccepted, map[string]any{"accepted": true})
	})
}

type approvalRequest struct {
	PlanDigest string `json:"plan_digest"`
}

func (s *server) approve(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	request, err := decode[approvalRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	actor := r.Header.Get("X-Software-Factory-Actor")
	if actor == "" {
		actor = "local-user"
	}
	if err = s.factory.Approve(r.Context(), r.PathValue("id"), actor, request.PlanDigest); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	request, err := decode[factory.SendMessageRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	actor := r.Header.Get("X-Software-Factory-Actor")
	if actor == "" {
		actor = "local-user"
	}
	value, err := s.factory.SendMessage(r.Context(), r.PathValue("id"), actor, request)
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, value)
}

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) retry(w http.ResponseWriter, r *http.Request) {
	request, err := decode[factory.RetryRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	value, err := s.factory.Retry(r.Context(), r.PathValue("id"), r.PathValue("attemptID"), request)
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, value)
}

func (s *server) interventions(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Interventions(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.factory.Delete(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *server) attempts(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Phases(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) attempt(w http.ResponseWriter, r *http.Request) {
	value, err := s.db.PhaseByID(r.Context(), r.PathValue("id"), r.PathValue("attemptID"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (s *server) branches(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Branches(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) artifacts(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Artifacts(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) checks(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Checks(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) results(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	values, err := s.db.Envelopes(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *server) diff(w http.ResponseWriter, r *http.Request) {
	value, err := s.factory.Diff(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	var values []store.Event
	var err error
	if tail > 0 {
		values, err = s.db.RecentEvents(r.Context(), r.PathValue("id"), tail)
	} else {
		values, err = s.db.Events(r.Context(), r.PathValue("id"), after, limit)
	}
	if err != nil {
		internal(w, err)
		return
	}
	cursor := after
	if len(values) > 0 {
		cursor = values[len(values)-1].Sequence
	}
	write(w, http.StatusOK, map[string]any{"events": values, "cursor": cursor, "format_version": session.FormatVersion})
}

func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	if !s.exists(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "stream_unsupported", "streaming unavailable")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		if value, err := strconv.ParseInt(header, 10, 64); err == nil && value > after {
			after = value
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ticker := time.NewTicker(500 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer heartbeat.Stop()
	send := func() bool {
		events, err := s.db.Events(r.Context(), r.PathValue("id"), after, 250)
		if err != nil {
			return false
		}
		for _, event := range events {
			body, _ := json.Marshal(event)
			fmt.Fprintf(w, "id: %d\nevent: event\ndata: %s\n\n", event.Sequence, body)
			after = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (s *server) exists(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.db.Task(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return false
	}
	return true
}

func (s *server) ready(w http.ResponseWriter) bool {
	if s.loadError != nil || len(s.validationErrors) > 0 {
		fail(w, http.StatusUnprocessableEntity, "configuration_invalid", "factory configuration is invalid")
		return false
	}
	return true
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func decode[T any](r *http.Request) (T, error) {
	var value T
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, fmt.Errorf("request must contain one JSON object")
		}
		return value, fmt.Errorf("decode trailing JSON: %w", err)
	}
	return value, nil
}

func emptyBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return false
	}
	if strings.TrimSpace(string(body)) != "" {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", "control request body must be empty")
		return false
	}
	return true
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, APIError{Code: code, Message: message})
}

func internal(w http.ResponseWriter, err error) {
	fail(w, http.StatusInternalServerError, "internal_error", err.Error())
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrStaleBranch):
		fail(w, http.StatusConflict, "stale_branch", "selected branch head is stale; refresh lineage and reselect the action")
	case errors.Is(err, store.ErrStaleAnchor):
		fail(w, http.StatusConflict, "stale_anchor", "artifact anchor is stale; reselect the source content")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "invalid_state", "task state does not allow this operation")
	case errors.Is(err, factory.ErrStalePlan):
		fail(w, http.StatusConflict, "stale_plan", err.Error())
	case errors.Is(err, factory.ErrInvalidFeedback):
		fail(w, http.StatusUnprocessableEntity, "invalid_feedback", err.Error())
	default:
		if err != nil && containsInvalid(err.Error()) {
			fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
			return
		}
		internal(w, err)
	}
}

func containsInvalid(message string) bool {
	for _, prefix := range []string{"text is required", "plan_digest is required", "idempotency_key is required", "attempt input snapshot is required", "target accepts", "anchor ", "unknown anchor"} {
		if strings.Contains(message, prefix) {
			return true
		}
	}
	return false
}

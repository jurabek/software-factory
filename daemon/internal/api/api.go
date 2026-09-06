package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type Server struct {
	db               *store.DB
	factory          *factory.Service
	config           config.Config
	validationErrors []string
	loadError        error
	harnesses        []string
	models           func(context.Context, string) ([]config.Model, error)
	token            string
	daemonID         string
	remoteToken      string
}

type RemoteAccess struct {
	DaemonID string
	Token    string
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(db *store.DB, service *factory.Service, cfg config.Config, problems []string, loadErr error, harnesses []string, models func(context.Context, string) ([]config.Model, error), access RemoteAccess) (*Server, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	if harnesses == nil {
		harnesses = []string{"pi"}
	}
	if models == nil {
		models = func(context.Context, string) ([]config.Model, error) { return []config.Model{}, nil }
	}
	return &Server{db: db, factory: service, config: cfg, validationErrors: problems, loadError: loadErr, harnesses: harnesses, models: models, token: token, daemonID: access.DaemonID, remoteToken: access.Token}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/identity", s.identity)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/config", s.configRead)
	mux.HandleFunc("GET /api/v1/harnesses", s.harnessesRead)
	mux.HandleFunc("GET /api/v1/models", s.modelsRead)
	mux.HandleFunc("GET /api/v1/control", s.control)
	mux.HandleFunc("POST /api/v1/tasks", s.mutation(s.create))
	mux.HandleFunc("GET /api/v1/tasks", s.tasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.task)
	mux.HandleFunc("POST /api/v1/tasks/{id}/sessions", s.mutation(s.createSession))
	mux.HandleFunc("GET /api/v1/tasks/{id}/sessions", s.taskSessions)
	mux.HandleFunc("POST /api/v1/tasks/{id}/{command}", s.mutation(s.command))
	mux.HandleFunc("POST /api/v1/tasks/{id}/feedback", s.mutation(s.feedback))
	mux.HandleFunc("POST /api/v1/tasks/{id}/interventions", s.mutation(s.createIntervention))
	mux.HandleFunc("GET /api/v1/tasks/{id}/interventions", s.interventions)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.mutation(s.delete))
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts", s.attempts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts/{attemptID}", s.attempt)
	mux.HandleFunc("GET /api/v1/tasks/{id}/branches", s.branches)
	mux.HandleFunc("GET /api/v1/tasks/{id}/artifacts", s.artifacts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events/stream", s.stream)
	mux.HandleFunc("GET /api/v1/tasks/{id}/results", s.results)
	mux.HandleFunc("GET /api/v1/tasks/{id}/checks", s.checks)
	mux.HandleFunc("GET /api/v1/tasks/{id}/diff", s.diff)
	return headers(s.authenticateRemote(mux))
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) {
	if s.remoteToken != "" && !s.hasRemoteCredential(r) {
		fail(w, http.StatusUnauthorized, "invalid_credential", "daemon credential missing or invalid")
		return
	}
	write(w, http.StatusOK, map[string]string{"id": s.daemonID})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) configRead(w http.ResponseWriter, _ *http.Request) {
	problems := append([]string{}, s.validationErrors...)
	if s.loadError != nil {
		problems = append(problems, s.loadError.Error())
	}
	write(w, http.StatusOK, map[string]any{"config": s.config, "errors": problems})
}

func (s *Server) harnessesRead(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{"harnesses": s.harnesses})
}

func (s *Server) defaultHarness() string {
	if s.config.Defaults.CodingAgent != "" {
		return s.config.Defaults.CodingAgent
	}
	if len(s.harnesses) > 0 {
		return s.harnesses[0]
	}
	return "pi"
}

func (s *Server) modelsRead(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	if s.remoteToken != "" || !isLoopbackRequest(r) {
		fail(w, http.StatusForbidden, "local_access_only", "control token is available only over loopback")
		return
	}
	write(w, http.StatusOK, map[string]any{"enabled": true, "token": s.token})
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
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
	write(w, http.StatusCreated, task)
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	values, err := s.db.Tasks(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	value, err := s.db.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
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
	write(w, http.StatusCreated, session)
}

func (s *Server) taskSessions(w http.ResponseWriter, r *http.Request) {
	values, err := s.db.TaskSessions(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	id := r.PathValue("id")
	var err error
	switch r.PathValue("command") {
	case "start":
		err = s.factory.Start(r.Context(), id)
	case "approve":
		actor := r.Header.Get("X-Software-Factory-Actor")
		if actor == "" {
			actor = "local-user"
		}
		err = s.factory.Approve(r.Context(), id, actor)
	case "pause":
		err = s.factory.Pause(r.Context(), id)
	case "resume":
		err = s.factory.Resume(r.Context(), id)
	case "abort":
		err = s.factory.Abort(r.Context(), id)
	default:
		fail(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"accepted": true})
}

type feedbackRequest struct {
	Feedback   string `json:"feedback"`
	PlanDigest string `json:"current_plan_digest,omitempty"`
}

func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	request, err := decode[feedbackRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	actor := r.Header.Get("X-Software-Factory-Actor")
	if actor == "" {
		actor = "local-user"
	}
	err = s.factory.Feedback(r.Context(), r.PathValue("id"), actor, request.Feedback, request.PlanDigest)
	if errors.Is(err, factory.ErrStalePlan) {
		fail(w, http.StatusConflict, "stale_plan", err.Error())
		return
	}
	if errors.Is(err, factory.ErrInvalidFeedback) {
		fail(w, http.StatusUnprocessableEntity, "invalid_feedback", err.Error())
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *Server) createIntervention(w http.ResponseWriter, r *http.Request) {
	request, err := decode[factory.InterveneRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	actor := r.Header.Get("X-Software-Factory-Actor")
	if actor == "" {
		actor = "local-user"
	}
	value, err := s.factory.Intervene(r.Context(), r.PathValue("id"), actor, request)
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, value)
}

func (s *Server) interventions(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.factory.Delete(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) attempts(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) attempt(w http.ResponseWriter, r *http.Request) {
	value, err := s.db.PhaseByID(r.Context(), r.PathValue("id"), r.PathValue("attemptID"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) checks(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	value, err := s.factory.Diff(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
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
	write(w, http.StatusOK, map[string]any{"events": values, "cursor": cursor})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) exists(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.db.Task(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return false
	}
	return true
}

func (s *Server) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if remoteAuthenticated(r.Context()) {
			next(w, r)
			return
		}
		provided := r.Header.Get("X-Software-Factory-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			fail(w, http.StatusForbidden, "invalid_token", "mutation token missing or invalid")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
			fail(w, http.StatusForbidden, "foreign_origin", "foreign origin rejected")
			return
		}
		next(w, r)
	}
}

type remoteAuthenticationKey struct{}

func (s *Server) authenticateRemote(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.remoteToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.hasRemoteCredential(r) {
			fail(w, http.StatusUnauthorized, "invalid_credential", "daemon credential missing or invalid")
			return
		}
		ctx := context.WithValue(r.Context(), remoteAuthenticationKey{}, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) hasRemoteCredential(r *http.Request) bool {
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	provided := strings.TrimPrefix(authorization, prefix)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.remoteToken)) == 1
}

func remoteAuthenticated(ctx context.Context) bool {
	authenticated, _ := ctx.Value(remoteAuthenticationKey{}).(bool)
	return authenticated
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *Server) ready(w http.ResponseWriter) bool {
	if s.loadError != nil || len(s.validationErrors) > 0 {
		fail(w, http.StatusUnprocessableEntity, "configuration_invalid", "factory configuration is invalid")
		return false
	}
	return true
}

func sameOrigin(origin string, r *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host == r.Host && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
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
		fail(w, http.StatusNotFound, "not_found", "task not found")
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
		if err != nil && (containsInvalid(err.Error())) {
			fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
			return
		}
		internal(w, err)
	}
}

func containsInvalid(message string) bool {
	for _, prefix := range []string{"intent ", "message is required", "idempotency_key is required", "target accepts", "anchor ", "unknown anchor", "delivery is rejected", "intent is required"} {
		if len(message) >= len(prefix) && message[:len(prefix)] == prefix {
			return true
		}
		if len(prefix) > 0 && containsSubstring(message, prefix) {
			return true
		}
	}
	return false
}

func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func newToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

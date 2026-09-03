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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jurabek/software-factory/internal/config"
	"github.com/jurabek/software-factory/internal/factory"
	"github.com/jurabek/software-factory/internal/store"
)

type Server struct {
	db               *store.DB
	factory          *factory.Service
	config           config.Config
	validationErrors []string
	loadError        error
	models           func(context.Context) ([]config.Model, error)
	token            string
}
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(db *store.DB, service *factory.Service, cfg config.Config, problems []string, loadErr error, models func(context.Context) ([]config.Model, error)) (*Server, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	return &Server{db: db, factory: service, config: cfg, validationErrors: problems, loadError: loadErr, models: models, token: token}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/config", s.configRead)
	mux.HandleFunc("GET /api/v1/models", s.modelsRead)
	mux.HandleFunc("GET /api/v1/control", s.control)
	mux.HandleFunc("POST /api/v1/campaigns", s.mutation(s.create))
	mux.HandleFunc("GET /api/v1/campaigns", s.campaigns)
	mux.HandleFunc("GET /api/v1/campaigns/{id}", s.campaign)
	mux.HandleFunc("POST /api/v1/campaigns/{id}/{command}", s.mutation(s.command))
	mux.HandleFunc("DELETE /api/v1/campaigns/{id}", s.mutation(s.delete))
	mux.HandleFunc("GET /api/v1/campaigns/{id}/phases", s.phases)
	mux.HandleFunc("GET /api/v1/campaigns/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/campaigns/{id}/events/stream", s.stream)
	mux.HandleFunc("GET /api/v1/campaigns/{id}/results", s.results)
	mux.HandleFunc("GET /api/v1/campaigns/{id}/checks", s.checks)
	mux.HandleFunc("GET /api/v1/campaigns/{id}/diff", s.diff)
	return headers(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	problems := append([]string{}, s.validationErrors...)
	if s.loadError != nil {
		status = "degraded"
		problems = append(problems, s.loadError.Error())
	}
	if _, err := s.models(r.Context()); err != nil {
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
func (s *Server) modelsRead(w http.ResponseWriter, r *http.Request) {
	models, err := s.models(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "models_unavailable", err.Error())
		return
	}
	write(w, http.StatusOK, map[string]any{"models": models})
}
func (s *Server) control(w http.ResponseWriter, _ *http.Request) {
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
	campaign, err := s.factory.Create(r.Context(), request)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_repository", err.Error())
		return
	}
	write(w, http.StatusCreated, campaign)
}
func (s *Server) campaigns(w http.ResponseWriter, r *http.Request) {
	values, err := s.db.Campaigns(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}
func (s *Server) campaign(w http.ResponseWriter, r *http.Request) {
	value, err := s.db.Campaign(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
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
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.factory.Delete(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"deleted": true})
}
func (s *Server) phases(w http.ResponseWriter, r *http.Request) {
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
	values, err := s.db.Events(r.Context(), r.PathValue("id"), after, limit)
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
	if _, err := s.db.Campaign(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return false
	}
	return true
}
func (s *Server) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		fail(w, http.StatusNotFound, "not_found", "campaign not found")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "invalid_state", "campaign state does not allow this operation")
	default:
		internal(w, err)
	}
}
func newToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

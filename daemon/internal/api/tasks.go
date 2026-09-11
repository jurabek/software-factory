package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/factory"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type tasksHandler struct {
	*server
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

type approvalRequest struct {
	PlanDigest string `json:"plan_digest"`
}

func (h tasksHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tasks", h.create)
	mux.HandleFunc("GET /api/v1/tasks", h.tasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", h.task)
	mux.HandleFunc("POST /api/v1/tasks/{id}/sessions", h.createSession)
	mux.HandleFunc("GET /api/v1/tasks/{id}/sessions", h.taskSessions)
	mux.HandleFunc("POST /api/v1/tasks/{id}/messages", h.sendMessage)
	mux.HandleFunc("GET /api/v1/tasks/{id}/messages", h.messages)
	mux.HandleFunc("POST /api/v1/tasks/{id}/attempts/{attemptID}/retry", h.retry)
	mux.HandleFunc("POST /api/v1/tasks/{id}/approve", h.approve)
	mux.Handle("POST /api/v1/tasks/{id}/pause", h.control(func(ctx context.Context, id string) error { return h.factory.Pause(ctx, id) }))
	mux.Handle("POST /api/v1/tasks/{id}/resume", h.control(func(ctx context.Context, id string) error { return h.factory.Resume(ctx, id) }))
	mux.Handle("POST /api/v1/tasks/{id}/abort", h.control(func(ctx context.Context, id string) error { return h.factory.Abort(ctx, id) }))
	mux.HandleFunc("GET /api/v1/tasks/{id}/interventions", h.interventions)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", h.delete)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts", h.attempts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/attempts/{attemptID}", h.attempt)
	mux.HandleFunc("GET /api/v1/tasks/{id}/branches", h.branches)
	mux.HandleFunc("GET /api/v1/tasks/{id}/artifacts", h.artifacts)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", h.events)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events/stream", h.stream)
	mux.HandleFunc("GET /api/v1/tasks/{id}/results", h.results)
	mux.HandleFunc("GET /api/v1/tasks/{id}/checks", h.checks)
	mux.HandleFunc("GET /api/v1/tasks/{id}/diff", h.diff)
}

func (h tasksHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	request, err := decode[factory.CreateRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	task, err := h.factory.Create(r.Context(), request)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			storeError(w, err)
			return
		}
		fail(w, http.StatusUnprocessableEntity, "invalid_task", err.Error())
		return
	}
	value, err := h.response(r.Context(), task)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusCreated, value)
}

func (h tasksHandler) tasks(w http.ResponseWriter, r *http.Request) {
	values, err := h.db.Tasks(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	response := make([]taskResponse, 0, len(values))
	for _, task := range values {
		value, viewErr := h.response(r.Context(), task)
		if viewErr != nil {
			internal(w, viewErr)
			return
		}
		response = append(response, value)
	}
	write(w, http.StatusOK, response)
}

func (h tasksHandler) task(w http.ResponseWriter, r *http.Request) {
	value, err := h.db.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	response, err := h.response(r.Context(), value)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, response)
}

func (h tasksHandler) createSession(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
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
	session, err := h.factory.CreateSession(r.Context(), r.PathValue("id"), request)
	if err != nil {
		storeError(w, err)
		return
	}
	value, err := h.response(r.Context(), session)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusCreated, value)
}

func (h tasksHandler) response(ctx context.Context, task store.Task) (taskResponse, error) {
	phases, err := h.db.Phases(ctx, task.ID)
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
	if h.factory != nil {
		stages, projectionErr := h.factory.StageProjection(ctx, task)
		if projectionErr != nil {
			return taskResponse{}, projectionErr
		}
		task.Stages = stages
	}
	return taskResponse{Task: task, AvailableActions: factory.AvailableActions(phase, task.State)}, nil
}

func (h tasksHandler) taskSessions(w http.ResponseWriter, r *http.Request) {
	values, err := h.db.TaskSessionsWithAgents(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	response := make([]taskSessionResponse, 0, len(values))
	for _, session := range values {
		view, viewErr := h.response(r.Context(), session.Task)
		if viewErr != nil {
			internal(w, viewErr)
			return
		}
		response = append(response, taskSessionResponse{Task: view.Task, AgentSessions: session.AgentSessions, AvailableActions: view.AvailableActions})
	}
	write(w, http.StatusOK, response)
}

func (h tasksHandler) control(action func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) || !emptyBody(w, r) {
			return
		}
		if err := action(r.Context(), r.PathValue("id")); err != nil {
			storeError(w, err)
			return
		}
		write(w, http.StatusAccepted, map[string]any{"accepted": true})
	})
}

func (h tasksHandler) approve(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
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
	if err = h.factory.Approve(r.Context(), r.PathValue("id"), actor, request.PlanDigest); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (h tasksHandler) sendMessage(w http.ResponseWriter, r *http.Request) {
	request, err := decode[factory.SendMessageRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	actor := r.Header.Get("X-Software-Factory-Actor")
	if actor == "" {
		actor = "local-user"
	}
	value, err := h.factory.SendMessage(r.Context(), r.PathValue("id"), actor, request)
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, value)
}

func (h tasksHandler) messages(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Messages(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) retry(w http.ResponseWriter, r *http.Request) {
	request, err := decode[factory.RetryRequest](r)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	value, err := h.factory.Retry(r.Context(), r.PathValue("id"), r.PathValue("attemptID"), request)
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusAccepted, value)
}

func (h tasksHandler) interventions(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Interventions(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.factory.Delete(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h tasksHandler) attempts(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Phases(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) attempt(w http.ResponseWriter, r *http.Request) {
	value, err := h.db.PhaseByID(r.Context(), r.PathValue("id"), r.PathValue("attemptID"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (h tasksHandler) branches(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Branches(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) artifacts(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Artifacts(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) checks(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Checks(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) results(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	values, err := h.db.Envelopes(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, err)
		return
	}
	write(w, http.StatusOK, values)
}

func (h tasksHandler) diff(w http.ResponseWriter, r *http.Request) {
	value, err := h.factory.Diff(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, http.StatusOK, value)
}

func (h tasksHandler) events(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	var values []store.Event
	var err error
	if tail > 0 {
		values, err = h.db.RecentEvents(r.Context(), r.PathValue("id"), tail)
	} else {
		values, err = h.db.Events(r.Context(), r.PathValue("id"), after, limit)
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

func (h tasksHandler) stream(w http.ResponseWriter, r *http.Request) {
	if !h.exists(w, r) {
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
		events, err := h.db.Events(r.Context(), r.PathValue("id"), after, 250)
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

func (h tasksHandler) exists(w http.ResponseWriter, r *http.Request) bool {
	if _, err := h.db.Task(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, err)
		return false
	}
	return true
}

func (h tasksHandler) ready(w http.ResponseWriter) bool {
	if h.loadError != nil || len(h.validationErrors) > 0 {
		fail(w, http.StatusUnprocessableEntity, "configuration_invalid", "factory configuration is invalid")
		return false
	}
	return true
}

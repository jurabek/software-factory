package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/factory"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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

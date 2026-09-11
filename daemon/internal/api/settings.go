package api

import (
	"net/http"
	"strings"
)

type settingsHandler struct {
	*server
}

func (h settingsHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/identity", h.identity)
	mux.HandleFunc("GET /api/v1/health", h.health)
	mux.HandleFunc("GET /api/v1/config", h.configRead)
	mux.HandleFunc("GET /api/v1/harnesses", h.harnessesRead)
	mux.HandleFunc("GET /api/v1/models", h.modelsRead)
	mux.HandleFunc("GET /api/v1/pipelines", h.pipelinesRead)
}

func (h settingsHandler) identity(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]string{"id": h.daemonID})
}

func (h settingsHandler) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	problems := append([]string{}, h.validationErrors...)
	if h.loadError != nil {
		status = "degraded"
		problems = append(problems, h.loadError.Error())
	}
	if _, err := h.models(r.Context(), h.defaultHarness()); err != nil {
		status = "degraded"
		problems = append(problems, err.Error())
	}
	write(w, http.StatusOK, map[string]any{"status": status, "errors": problems})
}

func (h settingsHandler) configRead(w http.ResponseWriter, _ *http.Request) {
	problems := append([]string{}, h.validationErrors...)
	if h.loadError != nil {
		problems = append(problems, h.loadError.Error())
	}
	write(w, http.StatusOK, map[string]any{"config": h.config, "errors": problems})
}

func (h settingsHandler) harnessesRead(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{"harnesses": h.harnesses})
}

func (h settingsHandler) defaultHarness() string {
	if h.config.Defaults.CodingAgent != "" {
		return h.config.Defaults.CodingAgent
	}
	if len(h.harnesses) > 0 {
		return h.harnesses[0]
	}
	return "pi"
}

func (h settingsHandler) modelsRead(w http.ResponseWriter, r *http.Request) {
	harness := strings.TrimSpace(r.URL.Query().Get("harness"))
	if harness == "" {
		harness = h.defaultHarness()
	}
	known := false
	for _, name := range h.harnesses {
		if name == harness {
			known = true
			break
		}
	}
	if !known {
		fail(w, http.StatusUnprocessableEntity, "unknown_harness", "harness "+harness+" is not available")
		return
	}
	models, err := h.models(r.Context(), harness)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "models_unavailable", err.Error())
		return
	}
	write(w, http.StatusOK, map[string]any{"harness": harness, "models": models})
}

func (h settingsHandler) pipelinesRead(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, h.config.Pipelines)
}

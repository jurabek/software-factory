package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSwaggerSpecDocumentsAPIRoutes(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/swagger.yaml", nil)
	response := httptest.NewRecorder()
	serveSwaggerSpec(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type = %q, want application/yaml", got)
	}

	var spec struct {
		Swagger  string                    `yaml:"swagger"`
		BasePath string                    `yaml:"basePath"`
		Paths    map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(response.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode swagger spec: %v", err)
	}
	if spec.Swagger != "2.0" {
		t.Fatalf("swagger = %q, want 2.0", spec.Swagger)
	}
	if spec.BasePath != "/api/v1" {
		t.Fatalf("basePath = %q, want /api/v1", spec.BasePath)
	}

	routes := map[string][]string{
		"/health":                   {"get"},
		"/config":                   {"get"},
		"/harnesses":                {"get"},
		"/models":                   {"get"},
		"/control":                  {"get"},
		"/tasks":                    {"get", "post"},
		"/tasks/{id}":               {"get", "delete"},
		"/tasks/{id}/start":         {"post"},
		"/tasks/{id}/approve":       {"post"},
		"/tasks/{id}/feedback":      {"post"},
		"/tasks/{id}/interventions": {"get", "post"},
		"/tasks/{id}/pause":         {"post"},
		"/tasks/{id}/resume":        {"post"},
		"/tasks/{id}/abort":         {"post"},
		"/tasks/{id}/attempts":      {"get"},
		"/tasks/{id}/events":        {"get"},
		"/tasks/{id}/events/stream": {"get"},
		"/tasks/{id}/results":       {"get"},
		"/tasks/{id}/checks":        {"get"},
		"/tasks/{id}/diff":          {"get"},
	}
	for path, methods := range routes {
		operations, ok := spec.Paths[path]
		if !ok {
			t.Errorf("missing path %s", path)
			continue
		}
		for _, method := range methods {
			if _, ok = operations[method]; !ok {
				t.Errorf("missing operation %s %s", method, path)
			}
		}
	}
}

func TestSwaggerUIUsesSameOriginAPI(t *testing.T) {
	handler := staticSecurityHeaders(http.HandlerFunc(serveSwaggerUI))
	request := httptest.NewRequest(http.MethodGet, "/docs", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	for _, expected := range []string{"/swagger.yaml", "/api/v1/control", "MutationToken"} {
		if !strings.Contains(body, expected) {
			t.Errorf("body missing %q", expected)
		}
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "https://unpkg.com") {
		t.Fatalf("documentation CSP does not allow Swagger UI assets: %q", policy)
	}
}

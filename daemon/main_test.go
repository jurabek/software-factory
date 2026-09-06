package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		"/identity":                 {"get"},
		"/health":                   {"get"},
		"/config":                   {"get"},
		"/harnesses":                {"get"},
		"/models":                   {"get"},
		"/control":                  {"get"},
		"/tasks":                    {"get", "post"},
		"/tasks/{id}":               {"get", "delete"},
		"/tasks/{id}/sessions":      {"get", "post"},
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
	if !strings.Contains(response.Body.String(), "DaemonCredential") {
		t.Fatal("swagger spec does not document daemon bearer authentication")
	}
	if !strings.Contains(response.Body.String(), "X-Software-Factory-Daemon-ID") {
		t.Fatal("swagger spec does not document expected daemon identity header")
	}
}

func TestDaemonIdentityPersistsInFactoryRoot(t *testing.T) {
	root := t.TempDir()
	first, err := loadDaemonID(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadDaemonID(root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 32 {
		t.Fatalf("identities = %q and %q", first, second)
	}
	info, err := os.Stat(filepath.Join(root, "daemon-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestMalformedDaemonIdentityFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "daemon-id"), []byte("not-an-identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDaemonID(root); err == nil {
		t.Fatal("loadDaemonID succeeded with malformed identity")
	}
}

func TestDaemonNetworkConfiguration(t *testing.T) {
	const token = "remote-test-credential-with-32-characters"
	for _, test := range []struct {
		name, bind, port, token, wantAddress string
		wantError                            bool
	}{
		{name: "loopback default", bind: "127.0.0.1", port: "8080", wantAddress: "127.0.0.1:8080"},
		{name: "IPv6 loopback", bind: "::1", port: "8080", wantAddress: "[::1]:8080"},
		{name: "remote bind with credential", bind: "0.0.0.0", port: "9000", token: token, wantError: true},
		{name: "remote without credential", bind: "0.0.0.0", port: "8080", wantError: true},
		{name: "weak credential", bind: "127.0.0.1", port: "8080", token: "short", wantError: true},
		{name: "hostname rejected", bind: "localhost", port: "8080", token: token, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			address, gotToken, err := daemonNetworkConfig(test.bind, test.port, test.token)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if address != test.wantAddress || gotToken != test.token && !test.wantError {
				t.Fatalf("address, token = %q, %q", address, gotToken)
			}
		})
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

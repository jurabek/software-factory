package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/token"
	"gopkg.in/yaml.v3"
)

func TestSwaggerSpecDocumentsAPIRoutes(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/swagger.yaml", nil)
	response := httptest.NewRecorder()
	newSwaggerHandler().serveSpec(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type = %q, want application/yaml", got)
	}

	var spec struct {
		Swagger     string                    `yaml:"swagger"`
		BasePath    string                    `yaml:"basePath"`
		Paths       map[string]map[string]any `yaml:"paths"`
		Definitions map[string]struct {
			AdditionalProperties *bool          `yaml:"additionalProperties"`
			Required             []string       `yaml:"required"`
			Properties           map[string]any `yaml:"properties"`
		} `yaml:"definitions"`
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
		"/identity":                              {"get"},
		"/health":                                {"get"},
		"/config":                                {"get"},
		"/harnesses":                             {"get"},
		"/models":                                {"get"},
		"/tasks":                                 {"get", "post"},
		"/tasks/{id}":                            {"get", "delete"},
		"/tasks/{id}/sessions":                   {"get", "post"},
		"/tasks/{id}/approve":                    {"post"},
		"/tasks/{id}/messages":                   {"get", "post"},
		"/tasks/{id}/interventions":              {"get"},
		"/tasks/{id}/pause":                      {"post"},
		"/tasks/{id}/resume":                     {"post"},
		"/tasks/{id}/abort":                      {"post"},
		"/tasks/{id}/attempts":                   {"get"},
		"/tasks/{id}/attempts/{attemptID}/retry": {"post"},
		"/tasks/{id}/events":                     {"get"},
		"/tasks/{id}/events/stream":              {"get"},
		"/tasks/{id}/results":                    {"get"},
		"/tasks/{id}/checks":                     {"get"},
		"/tasks/{id}/diff":                       {"get"},
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
	if _, exists := spec.Paths["/tasks/{id}/feedback"]; exists {
		t.Fatal("feedback write remains documented")
	}
	if _, exists := spec.Paths["/tasks/{id}/interventions"]["post"]; exists {
		t.Fatal("intervention write remains documented")
	}
	for _, name := range []string{"ApprovalRequest", "MessageTarget", "SendMessageRequest", "RetryRequest"} {
		definition := spec.Definitions[name]
		if definition.AdditionalProperties == nil || *definition.AdditionalProperties {
			t.Errorf("%s must reject unknown properties", name)
		}
	}
	if _, exists := spec.Definitions["Task"].Properties["available_actions"]; !exists {
		t.Fatal("Task.available_actions missing")
	}
	if !strings.Contains(response.Body.String(), "DaemonToken") {
		t.Fatal("swagger spec does not document daemon bearer authentication")
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

func TestDaemonTokenPersistsInFactoryRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon-token")
	first, err := loadDaemonToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadDaemonToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 32 {
		t.Fatalf("tokens = %q and %q", first, second)
	}
}

func TestAcquireLockExcludesAnotherDaemonForTheSameRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err = acquireLock(path); err == nil {
		t.Fatal("second daemon acquired the root lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
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

func TestBuildConnectionTokenCarriesEndpointAndCredential(t *testing.T) {
	const credential = "0123456789abcdef0123456789abcdef"
	const daemonID = "fedcba9876543210fedcba9876543210"
	signed, err := buildConnectionToken(daemonID, credential, "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := token.Parse(signed, credential)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != token.Issuer {
		t.Fatalf("issuer = %q, want %q", claims.Issuer, token.Issuer)
	}
	if claims.Subject != daemonID {
		t.Fatalf("sub = %q, want %q", claims.Subject, daemonID)
	}
	if claims.Endpoint != "http://127.0.0.1:8080" {
		t.Fatalf("endpoint = %q, want http://127.0.0.1:8080", claims.Endpoint)
	}
	if claims.Cred != credential {
		t.Fatalf("cred = %q, want %q", claims.Cred, credential)
	}
	hostname, _ := os.Hostname()
	if hostname != "" && claims.Name != hostname {
		t.Fatalf("name = %q, want hostname %q", claims.Name, hostname)
	}
}

func TestDaemonNetworkConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, bind, port, wantAddress string
		wantError                     bool
	}{
		{name: "loopback default", bind: "127.0.0.1", port: "8080", wantAddress: "127.0.0.1:8080"},
		{name: "IPv6 loopback", bind: "::1", port: "8080", wantAddress: "[::1]:8080"},
		{name: "remote bind", bind: "0.0.0.0", port: "9000", wantError: true},
		{name: "hostname rejected", bind: "localhost", port: "8080", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			address, err := daemonNetworkConfig(test.bind, test.port)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if !test.wantError && address != test.wantAddress {
				t.Fatalf("address = %q, want %q", address, test.wantAddress)
			}
		})
	}
}

func TestSwaggerUIUsesSameOriginAPI(t *testing.T) {
	handler := staticSecurityHeaders(http.HandlerFunc(newSwaggerHandler().serveUI))
	request := httptest.NewRequest(http.MethodGet, "/docs", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if !strings.Contains(body, "/swagger.yaml") {
		t.Errorf("body missing /swagger.yaml")
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "https://unpkg.com") {
		t.Fatalf("documentation CSP does not allow Swagger UI assets: %q", policy)
	}
}

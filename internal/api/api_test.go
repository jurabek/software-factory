package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/internal/config"
	"github.com/jurabek/software-factory/internal/store"
)

func TestEmptyCollectionsAreJSONArrays(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, func(context.Context) ([]config.Model, error) { return []config.Model{}, nil })
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ path, want string }{
		{path: "/api/v1/campaigns", want: "[]\n"},
		{path: "/api/v1/health", want: "{\"errors\":[],\"status\":\"ok\"}\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
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

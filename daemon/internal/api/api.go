package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
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
	settingsHandler{server: s}.registerRoutes(mux)
	tasksHandler{server: s}.registerRoutes(mux)
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

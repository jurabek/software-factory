package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jurabek/software-factory/internal/api"
	"github.com/jurabek/software-factory/internal/config"
	"github.com/jurabek/software-factory/internal/factory"
	factorygit "github.com/jurabek/software-factory/internal/git"
	"github.com/jurabek/software-factory/internal/harness"
	piharness "github.com/jurabek/software-factory/internal/harness/pi"
	"github.com/jurabek/software-factory/internal/store"
)

//go:embed web/dist/*
var frontend embed.FS

//go:embed templates
var defaultTemplates embed.FS

const defaultPort = "8080"

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	level := slog.LevelInfo
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	root, err := factoryRoot()
	if err != nil {
		return err
	}
	if err = bootstrap(root); err != nil {
		return fmt.Errorf("bootstrap factory: %w", err)
	}
	lock, err := acquireLock(filepath.Join(root, "server.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Recover(context.Background()); err != nil {
		return fmt.Errorf("recover stale runs: %w", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configured, problems, loadErr := config.Load(configPath)
	piPath := envOrDefault("PI_PATH", "pi")
	catalog := func(ctx context.Context) ([]config.Model, error) {
		return config.Catalog(ctx, config.OSRunner{}, piPath)
	}
	if loadErr == nil {
		if models, modelErr := catalog(context.Background()); modelErr != nil {
			problems = append(problems, modelErr.Error())
		} else {
			for _, agent := range configured.Agents {
				if _, resolveErr := config.ResolveModel(agent.Model, models); resolveErr != nil {
					problems = append(problems, agent.Name+": "+resolveErr.Error())
				}
			}
		}
	}
	registry := harness.Registry{"pi": piharness.Harness{Path: piPath}}
	service := factory.NewService(root, db, configured, configPath, registry, factorygit.OSRunner{})
	apiServer, err := api.New(db, service, configured, problems, loadErr, catalog)
	if err != nil {
		return err
	}
	staticFS, err := fs.Sub(frontend, "web/dist")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", apiServer.Handler())
	mux.Handle("/", spaHandler{files: staticFS})
	address := "127.0.0.1:" + envOrDefault("PORT", defaultPort)
	server := &http.Server{
		Addr:              address,
		Handler:           requestLog(logger, staticSecurityHeaders(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		logger.Info("server started", "address", "http://"+address, "root", root, "validation_errors", len(problems))
		done <- server.ListenAndServe()
	}()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		service.Shutdown(shutdownCtx)
		return server.Shutdown(shutdownCtx)
	}
}

func factoryRoot() (string, error) {
	if root := os.Getenv("SOFTWARE_FACTORY_DIR"); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".software-factory"), nil
}

func bootstrap(root string) error {
	for _, dir := range []string{root, filepath.Join(root, "campaigns")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return installTemplates(root)
}

func installTemplates(root string) error {
	return fs.WalkDir(defaultTemplates, "templates", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel("templates", path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(root, filepath.FromSlash(relative))
		if entry.IsDir() {
			if err = os.MkdirAll(destination, 0o700); err != nil {
				return err
			}
			return os.Chmod(destination, 0o700)
		}
		data, err := defaultTemplates.ReadFile(path)
		if err != nil {
			return err
		}
		return createIfMissing(destination, data)
	})
}

func createIfMissing(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

type fileLock struct{ *os.File }

func acquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("server already running: %w", err)
	}
	if err = file.Truncate(0); err != nil {
		file.Close()
		return nil, err
	}
	if _, err = file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		file.Close()
		return nil, err
	}
	return &fileLock{file}, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type spaHandler struct{ files fs.FS }

func (handler spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Path)
	if path == "." || path == "/" {
		path = "/index.html"
	}
	if _, err := fs.Stat(handler.files, path[1:]); err != nil {
		path = "/index.html"
	}
	http.ServeFileFS(w, r, handler.files, path)
}

func staticSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

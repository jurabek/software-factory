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
	"strings"
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

const defaultPort = "8080"

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(strings.ToUpper(envOrDefault("LOG_LEVEL", "INFO"))))
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
	server := &http.Server{Addr: address, Handler: requestLog(logger, staticSecurityHeaders(mux)), ReadHeaderTimeout: 5 * time.Second}
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
	for _, dir := range []string{root, filepath.Join(root, "prompts"), filepath.Join(root, "campaigns")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	configData := []byte("defaults:\n  coding_agent: pi\n  model: cursor/gpt-5.6-luna\n  thinking: medium\n  tools: [read, grep, find, ls]\nobservability:\n  poll_ms: 500\nruntime:\n  agent_deadline_ms: 1800000\n  empty_turn_retries: 2\n  json_fix_attempts: 2\nagents:\n  - name: planner\n    model: cursor/gpt-5.6-sol\n    thinking: high\n    color: \"#a78bfa\"\n    purpose: Turn a request into a plan the builder can implement without asking questions.\n    prompt_engineering: {system: prompts/planner/system.md, user: prompts/planner/user.md}\n  - name: builder\n    color: \"#22d3ee\"\n    purpose: Implement the approved plan exactly and report every changed file.\n    prompt_engineering: {system: prompts/builder/system.md, user: prompts/builder/user.md}\n    tools: [read, grep, find, ls, edit, write]\n  - name: reviewer\n    thinking: low\n    color: \"#fb7185\"\n    purpose: Review the implementation without changing files.\n    prompt_engineering: {system: prompts/reviewer/system.md, user: prompts/reviewer/user.md}\n")
	if err := createIfMissing(filepath.Join(root, "config.yaml"), configData); err != nil {
		return err
	}
	prompts := map[string][2]string{"planner": {"You are the read-only Planner. Return only the required planner JSON envelope.", "Feature request:\n{{.Request}}\n\nInspect {{.Repository}} and produce concrete steps with expected_files and acceptance_criteria."}, "builder": {"You are the Builder. Implement the approved plan. Return only the required builder JSON envelope.", "Feature request:\n{{.Request}}\n\nApproved plan:\n{{.Plan}}\n\nImplement it. Do not commit, push, merge, or deploy."}, "reviewer": {"You are the read-only Reviewer. Return only the required reviewer JSON envelope.", "Feature request:\n{{.Request}}\n\nApproved plan:\n{{.Plan}}\n\nChecks:\n{{.Checks}}\n\nGit-derived changed files:\n{{.ChangedFiles}}"}}
	for role, body := range prompts {
		dir := filepath.Join(root, "prompts", role)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := createIfMissing(filepath.Join(dir, "system.md"), []byte(body[0])); err != nil {
			return err
		}
		if err := createIfMissing(filepath.Join(dir, "user.md"), []byte(body[1])); err != nil {
			return err
		}
	}
	return nil
}
func createIfMissing(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
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

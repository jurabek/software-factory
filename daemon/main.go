package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/api"
	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	claudeharness "github.com/jurabek/software-factory/daemon/internal/harness/claude"
	piharness "github.com/jurabek/software-factory/daemon/internal/harness/pi"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/token"
)

//go:embed templates
var defaultTemplates embed.FS

const (
	defaultPort = "8080"
	defaultBind = "127.0.0.1"
)

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
	daemonID, err := loadDaemonID(root)
	if err != nil {
		return fmt.Errorf("load daemon identity: %w", err)
	}
	tokenPath := filepath.Join(root, "daemon-token")
	daemonToken, err := loadDaemonToken(tokenPath)
	if err != nil {
		return fmt.Errorf("load daemon token: %w", err)
	}
	address, err := daemonNetworkConfig(
		envOrDefault("SOFTWARE_FACTORY_BIND", defaultBind),
		envOrDefault("PORT", defaultPort),
	)
	if err != nil {
		return err
	}
	connectionTokenPath := filepath.Join(root, "connection-token")
	connectionToken, err := buildConnectionToken(daemonID, daemonToken, address)
	if err != nil {
		return fmt.Errorf("build connection token: %w", err)
	}
	if err = os.WriteFile(connectionTokenPath, []byte(connectionToken+"\n"), 0o600); err != nil {
		return fmt.Errorf("write connection token: %w", err)
	}
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
	claudePath := envOrDefault("CLAUDE_PATH", "claude")
	registry := harness.Registry{
		"pi":     piharness.Harness{Path: piPath},
		"claude": claudeharness.Harness{Config: claudeharness.Config{Path: claudePath, AllowedTools: configured.Claude.AllowedTools, Models: configured.Claude.Models}},
	}
	harnessNames := make([]string, 0, len(registry))
	for name := range registry {
		harnessNames = append(harnessNames, name)
	}
	sort.Strings(harnessNames)
	catalog := func(ctx context.Context, harnessName string) ([]config.Model, error) {
		if harnessName == "" || harnessName == "pi" {
			models, err := config.Catalog(ctx, config.OSRunner{}, piPath)
			if err != nil {
				return nil, err
			}
			for i := range models {
				models[i].Thinking = config.ThinkingLevelsFor("pi")
			}
			return models, nil
		}
		adapter, ok := registry.Get(harnessName)
		if !ok {
			return nil, fmt.Errorf("harness %s unavailable", harnessName)
		}
		models, err := adapter.Models(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]config.Model, 0, len(models))
		for _, m := range models {
			thinking := m.Thinking
			if len(thinking) == 0 {
				thinking = config.ThinkingLevelsFor(harnessName)
			}
			out = append(out, config.Model{Provider: m.Provider, ID: m.ID, ContextWindow: m.ContextWindow, Thinking: thinking})
		}
		return out, nil
	}
	if loadErr == nil {
		harnessForValidation := configured.Defaults.CodingAgent
		if harnessForValidation == "" {
			harnessForValidation = "pi"
		}
		if models, modelErr := catalog(context.Background(), harnessForValidation); modelErr != nil {
			problems = append(problems, modelErr.Error())
		} else {
			for _, agent := range configured.Agents {
				if _, resolveErr := config.ResolveModel(agent.Model, models); resolveErr != nil {
					problems = append(problems, agent.Name+": "+resolveErr.Error())
				}
				if !config.IsValidThinkingFor(harnessForValidation, agent.Thinking) {
					problems = append(problems, agent.Name+": thinking "+agent.Thinking+" unsupported for "+harnessForValidation)
				}
			}
		}
	}
	service := factory.NewService(root, factory.Dependencies{
		Store: db, Config: configured, ConfigPath: configPath,
		Harnesses: registry, Git: factorygit.OSRunner{},
	})
	apiHandler, err := api.New(db, service, configured, problems, loadErr, harnessNames, catalog, api.Access{DaemonID: daemonID, Token: daemonToken})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              address,
		Handler:           newServer(logger, apiHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stdout, "daemon token: %s\ndaemon token file: %s\n", daemonToken, tokenPath)
		fmt.Fprintf(os.Stdout, "connection token: %s\nconnection token file: %s\n", connectionToken, connectionTokenPath)
		logger.Info("server started", "address", "http://"+address, "root", root, "validation_errors", len(problems), "token_file", tokenPath)
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

// buildConnectionToken mints the single-paste connection token whose claims
// carry everything the application server needs to register this daemon: the
// endpoint (http://<bind>:<port> derived from the resolved listen address), the
// daemon id, the OS hostname, and the bearer credential. The credential doubles
// as the HMAC signing key so the token verifies without any shared secret.
func buildConnectionToken(daemonID, credential, address string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "software-factory-daemon"
	}
	claims := token.Claims{
		Issuer:   token.Issuer,
		Subject:  daemonID,
		Endpoint: "http://" + address,
		Name:     hostname,
		Cred:     credential,
		IssuedAt: time.Now().UTC().Unix(),
	}
	return token.Sign(claims, credential)
}

func daemonNetworkConfig(bind, port string) (string, error) {
	address := net.ParseIP(bind)
	if address == nil {
		return "", fmt.Errorf("SOFTWARE_FACTORY_BIND must be an IP address")
	}
	if !address.IsLoopback() {
		return "", fmt.Errorf("SOFTWARE_FACTORY_BIND must remain loopback; use an encrypted tunnel for remote access")
	}
	return net.JoinHostPort(bind, port), nil
}

func loadDaemonToken(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err == nil {
		return validateDaemonID(string(value))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	if err = createIfMissing(path, []byte(hex.EncodeToString(random[:])+"\n")); err != nil {
		return "", err
	}
	value, err = os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return validateDaemonID(string(value))
}

func loadDaemonID(root string) (string, error) {
	path := filepath.Join(root, "daemon-id")
	value, err := os.ReadFile(path)
	if err == nil {
		return validateDaemonID(string(value))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	if err = createIfMissing(path, []byte(hex.EncodeToString(random[:])+"\n")); err != nil {
		return "", err
	}
	value, err = os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return validateDaemonID(string(value))
}

func validateDaemonID(value string) (string, error) {
	id := strings.TrimSpace(value)
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || id != strings.ToLower(id) {
		return "", fmt.Errorf("daemon-id must contain 32 lowercase hexadecimal characters")
	}
	return id, nil
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
	for _, dir := range []string{root, filepath.Join(root, "tasks")} {
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

func newServer(logger *slog.Logger, apiHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", apiHandler)
	newSwaggerHandler().registerRoutes(mux)
	return requestLog(logger, staticSecurityHeaders(mux))
}

func staticSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		policy := "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'"
		if r.URL.Path == "/docs" || strings.HasPrefix(r.URL.Path, "/docs/") {
			policy = "default-src 'self'; connect-src 'self'; img-src 'self' data:; script-src 'self' 'unsafe-inline' https://unpkg.com; style-src 'self' 'unsafe-inline' https://unpkg.com"
		}
		w.Header().Set("Content-Security-Policy", policy)
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

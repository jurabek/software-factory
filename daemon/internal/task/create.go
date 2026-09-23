package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Create validates a request and persists a new task with an initial branch.
func (s *Service) Create(ctx context.Context, request CreateRequest) (store.Task, error) {
	task, err := s.create(ctx, request, "")
	if err != nil {
		return store.Task{}, err
	}
	if err = s.ensureBranch(ctx, task.ID, ""); err != nil {
		return store.Task{}, err
	}
	return task, nil
}

// CreateSession creates a child task reusing the parent's repository and agent
// configuration.
func (s *Service) CreateSession(ctx context.Context, taskID string, request CreateSessionRequest) (store.Task, error) {
	task, err := s.deps.Store.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.ParentTaskID != "" {
		task, err = s.deps.Store.Task(ctx, task.ParentTaskID)
		if err != nil {
			return store.Task{}, err
		}
	}
	repository := Repository{Type: task.RepositoryType}
	if task.RepositoryType == "local" {
		repository.Path = task.RepositorySource
	} else {
		repository.Repo = task.RepositorySource
	}
	created, err := s.create(ctx, CreateRequest{
		Request:     request.Request,
		Repository:  repository,
		CodingAgent: task.CodingAgent,
		Model:       task.Model,
		Thinking:    task.Thinking,
		Pipeline:    task.Pipeline,
	}, task.ID)
	if err != nil {
		return store.Task{}, err
	}
	if err = s.ensureBranch(ctx, created.ID, ""); err != nil {
		return store.Task{}, err
	}
	return created, nil
}

func (s *Service) create(ctx context.Context, request CreateRequest, parentTaskID string) (store.Task, error) {
	request.Request = strings.TrimSpace(request.Request)
	if request.Request == "" {
		return store.Task{}, fmt.Errorf("task description is required")
	}
	request.CodingAgent = strings.TrimSpace(request.CodingAgent)
	request.Model = strings.TrimSpace(request.Model)
	request.Thinking = strings.TrimSpace(request.Thinking)
	request.Pipeline = strings.TrimSpace(request.Pipeline)
	configured, err := config.Freeze(s.deps.Config, s.deps.ConfigPath)
	if err != nil {
		return store.Task{}, err
	}
	selectedPipeline, err := config.SelectPipeline(configured, request.Pipeline)
	if err != nil {
		return store.Task{}, err
	}
	if request.CodingAgent != "" && !config.IsValidHarness(request.CodingAgent) {
		return store.Task{}, fmt.Errorf("coding_agent must be pi or codex")
	}
	if request.Thinking != "" {
		harnessForThinking := request.CodingAgent
		if harnessForThinking == "" {
			harnessForThinking = s.deps.Config.Defaults.CodingAgent
		}
		if harnessForThinking == "" {
			harnessForThinking = "pi"
		}
		if !config.IsValidThinkingFor(harnessForThinking, request.Thinking) {
			return store.Task{}, fmt.Errorf("thinking %q unsupported for %s", request.Thinking, harnessForThinking)
		}
	}
	if request.CodingAgent != "" {
		if _, ok := s.deps.Harnesses.Get(request.CodingAgent); !ok && s.deps.Harnesses != nil {
			return store.Task{}, fmt.Errorf("harness %s unavailable", request.CodingAgent)
		}
	}
	id, err := taskID()
	if err != nil {
		return store.Task{}, err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	source, submitted, err := normalizeRepository(request.Repository)
	if err != nil {
		return store.Task{}, err
	}
	workspace := filepath.Join(s.root, "tasks", id)
	for _, directory := range []string{workspace, filepath.Join(workspace, "workspace", "repository"), filepath.Join(workspace, "attempts"), filepath.Join(workspace, "snapshots"), filepath.Join(workspace, "sessions"), filepath.Join(workspace, "workspace", "snapshots"), filepath.Join(workspace, "workspace", "branches"), filepath.Join(workspace, "workspace", "attempts")} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			_ = os.RemoveAll(workspace)
			return store.Task{}, fmt.Errorf("create task workspace: %w", err)
		}
	}
	configured = config.ApplyTaskOverrides(configured, request.CodingAgent, request.Model, request.Thinking)
	configSnapshot, err := config.Snapshot(configured)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task config: %w", err)
	}
	if len(configured.Agents) == 0 {
		configSnapshot = ""
	}
	task := store.Task{ID: id, ParentTaskID: parentTaskID, Request: request.Request, WorkspacePath: workspace, RepositoryType: request.Repository.Type, RepositorySource: source, SubmittedRepositoryPath: submitted, State: string(stagekit.Preparing), Pipeline: selectedPipeline.Name, ConfigSnapshot: configSnapshot, CreatedAt: createdAt, StartedAt: createdAt, CodingAgent: request.CodingAgent, Model: request.Model, Thinking: request.Thinking}
	metadata, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task metadata: %w", err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "task.json"), metadata, 0o600); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("write task metadata: %w", err)
	}
	if err := s.deps.Store.CreateActiveTask(ctx, task); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, err
	}
	return task, nil
}

func normalizeRepository(input Repository) (source, submitted string, err error) {
	switch input.Type {
	case "local":
		if !filepath.IsAbs(input.Path) {
			return "", "", fmt.Errorf("local repository path must be absolute")
		}
		source, submitted = input.Path, input.Path
	case "github":
		if parts := strings.Split(input.Repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("github repository must be owner/repository")
		}
		source = input.Repo
	default:
		return "", "", fmt.Errorf("repository type must be local or github")
	}
	return source, submitted, nil
}

func taskID() (string, error) {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "SF-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(bytes[:]), nil
}

// Package task owns task identity and lifecycle at the data layer: creation,
// filesystem layout, metadata, repository normalization, branch selection,
// deletion, and diffs. The orchestrator delegates task CRUD here.
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

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/workspace"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Repository describes the source a task is created from.
type Repository struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Repo string `json:"repo,omitempty"`
}

// CreateRequest is the control-plane request to create a task.
type CreateRequest struct {
	Request     string     `json:"request"`
	Repository  Repository `json:"repository"`
	Pipeline    string     `json:"pipeline,omitempty"`
	CodingAgent string     `json:"coding_agent,omitempty"`
	Model       string     `json:"model,omitempty"`
	Thinking    string     `json:"thinking,omitempty"`
}

// CreateSessionRequest starts a child task reusing the parent's repository and
// agent selection.
type CreateSessionRequest struct {
	Request string `json:"request"`
}

// Diff is the changed-file and patch view of a task branch.
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

// Deps are the collaborators a task service needs.
type Deps struct {
	Store      *store.Store
	Config     config.Config
	ConfigPath string
	Harnesses  harness.Registry
	Sandbox    workspace.Sandbox
}

// Service creates and manages task records and their filesystem.
type Service struct {
	root string
	deps Deps
}

// New constructs a task service rooted at the factory root.
func New(root string, deps Deps) *Service {
	return &Service{root: root, deps: deps}
}

func (s *Service) ensureBranch(ctx context.Context, taskID, parent string) error {
	task, err := s.deps.Store.Tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}

	if task.SelectedBranchID != "" {
		return nil
	}
	branches, err := s.deps.Store.Branches.List(ctx, taskID)
	if err != nil {
		return err
	}
	if len(branches) > 0 {
		return s.deps.Store.Branches.Select(
			ctx, taskID,
			branches[0].ID)
	}
	branch := store.Branch{ID: stagekit.RandomID(), TaskID: taskID, ParentBranchID: parent, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = s.deps.Store.Branches.Create(ctx, branch); err != nil {
		return err
	}
	return s.deps.Store.Branches.Select(ctx, taskID, branch.ID)
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (store.Task, error) {
	task, err := s.create(ctx, request, "")
	if err != nil {
		return store.Task{}, err
	}
	if err = s.ensureBranch(ctx, task.ID, ""); err != nil {
		return store.Task{}, err
	}
	return task,
		nil
}

func (s *Service) CreateSession(ctx context.Context, taskID string, request CreateSessionRequest) (store.Task, error) {
	task, err := s.deps.Store.Tasks.Get(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.ParentTaskID != "" {
		task, err = s.deps.Store.Tasks.Get(
			ctx, task.ParentTaskID)
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
		Request:    request.Request,
		Repository: repository, CodingAgent: task.CodingAgent, Model: task.Model, Thinking: task.Thinking, Pipeline: task.Pipeline,
	},
		task.ID)
	if err != nil {
		return store.Task{}, err
	}
	if err = s.ensureBranch(ctx,
		created.ID,
		""); err != nil {
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
		return store.Task{}, fmt.Errorf("coding_agent must be pi")
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
			return store.Task{}, fmt.Errorf("thinking %q unsupported for %s",
				request.Thinking, harnessForThinking)
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
	source,
		submitted, err := normalizeRepository(request.Repository)
	if err != nil {
		return store.Task{}, err
	}
	workspace := filepath.Join(s.root, "tasks", id)
	for _, directory := range []string{
		workspace, filepath.Join(workspace, "workspace",
			"repository"), filepath.Join(workspace, "attempts"),
		filepath.Join(workspace,
			"snapshots"), filepath.Join(workspace, "sessions"), filepath.Join(workspace, "workspace",
			"snapshots"), filepath.Join(workspace, "workspace", "branches"), filepath.Join(
			workspace, "workspace", "attempts"),
	} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			_ = os.RemoveAll(workspace)
			return store.Task{}, fmt.Errorf("create task workspace: %w",
				err)
		}
	}
	configured = config.ApplyTaskOverrides(configured, request.CodingAgent,
		request.Model,
		request.Thinking,
	)
	configSnapshot, err := config.Snapshot(configured)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task config: %w", err)
	}
	if len(configured.Agents) == 0 {
		configSnapshot = ""
	}
	task := store.Task{
		ID: id, ParentTaskID: parentTaskID, Request: request.Request,
		WorkspacePath:  workspace,
		RepositoryType: request.Repository.Type, RepositorySource: source, SubmittedRepositoryPath: submitted, State: stagekit.Preparing, Pipeline: selectedPipeline.Name, ConfigSnapshot: configSnapshot, CreatedAt: createdAt, StartedAt: createdAt, CodingAgent: request.CodingAgent, Model: request.Model, Thinking: request.Thinking,
	}
	metadata, err := json.MarshalIndent(task, "",
		"  ")
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task metadata: %w",
			err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "task.json"), metadata, 0o600); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("write task metadata: %w", err)
	}
	if err := s.deps.Store.Tasks.CreateActive(ctx, task); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, err
	}
	return task, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	task, err := s.deps.Store.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	tasks := []store.Task{task}
	if task.ParentTaskID == "" {
		tasks, err = s.deps.Store.Tasks.Sessions(ctx, id)
		if err != nil {
			return err
		}
	}
	for _, session := range tasks {
		if stagekit.IsActive(session.State) {
			return store.ErrConflict
		}
	}
	for _, session := range tasks {
		if s.deps.Sandbox != nil {
			_ = s.deps.Sandbox.Cleanup(ctx, workspace.CleanupRequest{TaskID: session.ID, WorkspaceRoot: session.WorkspacePath, SourceType: session.RepositoryType, CanonicalPath: session.CanonicalRepositoryPath, WorkingPath: session.RepositoryPath})
		}

		if err := os.RemoveAll(filepath.Join(s.root, "tasks", session.ID)); err != nil {
			return fmt.Errorf("remove task files: %w", err)
		}
	}
	return s.deps.Store.Tasks.Delete(ctx,
		id)
}

func (s *Service) Diff(ctx context.Context, id string) (Diff, error) {
	task, err := s.deps.Store.Tasks.Get(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	return s.diffRepository(task,
		false)
}

func (s *Service) diffRepository(task store.Task, reviewBase bool) (Diff, error) {
	base := task.BaseSHA
	if reviewBase && task.ReviewBaseSHA != "" {
		base = task.ReviewBaseSHA
	}
	files, err := factorygit.ChangedFiles(task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	patch, err := factorygit.Diff(
		task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	return Diff{Files: files, Patch: patch}, nil
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

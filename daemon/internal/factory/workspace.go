package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type workspaceStore interface {
	CreateWorkspaceOperation(context.Context, store.WorkspaceOperation) error
	UpdateWorkspaceOperation(context.Context, string, string, string) error
	SetRepositoryPrepared(context.Context, store.TaskRepository) error
	SetRepositoryReviewBase(context.Context, string, string, string) error
	SetRepositoryBranch(context.Context, string, string, string) error
}

type workspaceLifecycle interface {
	Allocate(context.Context, store.Task) error
	Prepare(context.Context, store.Task) ([]workspacePreparation, error)
	InspectProfiles(context.Context, store.Task) (map[string]Materialization, error)
	CaptureSnapshot(context.Context, store.Task) (store.WorkspaceSnapshot, error)
	MaterializeSnapshot(context.Context, store.Task, string) error
	MaterializeScratch(context.Context, store.Task, string, string) error
	Restore(context.Context, store.Task, store.Phase, string) ([]store.PhaseRepositoryInput, error)
	Cleanup(context.Context, store.Task) error
}

type workspaceService struct {
	root      string
	db        workspaceStore
	sandbox   Sandbox
	snapshots *snapshotService
	git       factorygit.Runner
	gitMu     sync.Mutex
}

type workspacePreparation struct {
	Repository store.TaskRepository
	Profile    Materialization
}

func (w *workspaceService) Allocate(ctx context.Context, task store.Task) error {
	if err := w.validateTaskWorkspace(task); err != nil {
		return err
	}
	operation, err := w.beginOperation(ctx, task.ID, "", "", "allocate", task.WorkspacePath)
	if err != nil {
		return err
	}
	for _, directory := range workspaceDirectories(task.WorkspacePath) {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			return w.failOperation(ctx, operation.ID, fmt.Errorf("create task workspace: %w", err))
		}
	}
	metadata, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return w.failOperation(ctx, operation.ID, fmt.Errorf("encode task metadata: %w", err))
	}
	if err = os.WriteFile(filepath.Join(task.WorkspacePath, "task.json"), metadata, 0o600); err != nil {
		return w.failOperation(ctx, operation.ID, fmt.Errorf("write task metadata: %w", err))
	}
	return w.completeOperation(ctx, operation.ID)
}

func (w *workspaceService) Prepare(ctx context.Context, task store.Task) ([]workspacePreparation, error) {
	if err := w.validateTaskWorkspace(task); err != nil {
		return nil, err
	}
	prepared := make([]workspacePreparation, 0, len(task.Repositories))
	for _, repository := range task.Repositories {
		value, err := w.prepareRepository(ctx, task, repository)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, value)
	}
	return prepared, nil
}

func (w *workspaceService) prepareRepository(ctx context.Context, task store.Task, repository store.TaskRepository) (workspacePreparation, error) {
	destination := filepath.Join(task.WorkspacePath, "workspace", "repositories", repository.Name)
	profilePath := filepath.Join(task.WorkspacePath, "repository-profiles", repository.Name+".json")
	if repository.WorkingPath != "" {
		if _, statErr := os.Stat(destination); statErr == nil {
			if profile, err := readMaterialization(profilePath); err == nil {
				repository.CanonicalPath = profile.Root
				repository.WorkingPath = destination
				repository.BaseSHA = profile.BaseSHA
				repository.ReviewBaseSHA = profile.BaseSHA
				repository.BranchName = profile.BranchName
				if err = w.db.SetRepositoryPrepared(ctx, repository); err != nil {
					return workspacePreparation{}, err
				}
				return workspacePreparation{Repository: repository, Profile: profile}, nil
			}
		}
	}
	request := MaterializationRequest{TaskID: task.ID, RepositoryID: repository.ID, Name: repository.Name, SourceType: repository.SourceType, Source: repository.SourceValue, Destination: destination}
	operation, err := w.beginOperation(ctx, task.ID, repository.ID, "", "materialize", request)
	if err != nil {
		return workspacePreparation{}, err
	}
	if w.sandbox == nil {
		return workspacePreparation{}, w.failOperation(ctx, operation.ID, fmt.Errorf("sandbox unavailable"))
	}
	w.gitMu.Lock()
	profile, err := w.sandbox.Materialize(ctx, request)
	w.gitMu.Unlock()
	if err != nil {
		return workspacePreparation{}, w.failOperation(ctx, operation.ID, err)
	}
	repository.CanonicalPath = profile.Root
	repository.WorkingPath = destination
	repository.BaseSHA = profile.BaseSHA
	repository.ReviewBaseSHA = profile.BaseSHA
	repository.BranchName = profile.BranchName
	if err = w.db.SetRepositoryPrepared(ctx, repository); err != nil {
		return workspacePreparation{}, w.failOperation(ctx, operation.ID, err)
	}
	if err = writeRepositoryProfile(task.WorkspacePath, repository.Name, profile); err != nil {
		return workspacePreparation{}, w.failOperation(ctx, operation.ID, err)
	}
	if err = w.completeOperation(ctx, operation.ID); err != nil {
		return workspacePreparation{}, err
	}
	return workspacePreparation{Repository: repository, Profile: profile}, nil
}

func (w *workspaceService) InspectProfiles(_ context.Context, task store.Task) (map[string]Materialization, error) {
	profiles := make(map[string]Materialization, len(task.Repositories))
	for _, repository := range task.Repositories {
		profile, err := readMaterialization(filepath.Join(task.WorkspacePath, "repository-profiles", repository.Name+".json"))
		if err != nil {
			return nil, fmt.Errorf("read repository profile %s: %w", repository.Name, err)
		}
		profiles[repository.Name] = profile
	}
	return profiles, nil
}

func (w *workspaceService) CaptureSnapshot(ctx context.Context, task store.Task) (store.WorkspaceSnapshot, error) {
	return w.snapshots.CaptureSnapshot(ctx, task)
}

func (w *workspaceService) MaterializeSnapshot(ctx context.Context, task store.Task, digest string) error {
	return w.snapshots.MaterializeSnapshot(ctx, task, digest)
}

func (w *workspaceService) MaterializeScratch(ctx context.Context, task store.Task, digest, destination string) error {
	return w.snapshots.MaterializeScratch(ctx, task, digest, destination)
}

func (w *workspaceService) Restore(ctx context.Context, task store.Task, phase store.Phase, key string) ([]store.PhaseRepositoryInput, error) {
	if err := w.validateTaskWorkspace(task); err != nil {
		return nil, err
	}
	inputs, err := w.phaseInputs(ctx, phase.ID)
	if err != nil {
		return nil, err
	}
	restored := make([]store.PhaseRepositoryInput, 0, len(inputs))
	for _, input := range inputs {
		repository, ok := repositoryByID(task.Repositories, input.RepositoryID)
		if !ok {
			return nil, fmt.Errorf("retry repository %s is missing", input.RepositoryID)
		}
		branch := retryBranch(task.ID, key, repository.ID)
		request := struct {
			RepositoryID string `json:"repository_id"`
			HeadSHA      string `json:"head_sha"`
			Branch       string `json:"branch"`
		}{RepositoryID: repository.ID, HeadSHA: input.HeadSHA, Branch: branch}
		operation, operationErr := w.beginOperation(ctx, task.ID, repository.ID, phase.ID, "restore", request)
		if operationErr != nil {
			return nil, operationErr
		}
		w.gitMu.Lock()
		err = factorygit.RestoreForRetry(ctx, w.git, repository.SourceType, repository.CanonicalPath, repository.WorkingPath, input.HeadSHA, branch)
		w.gitMu.Unlock()
		if err != nil {
			return nil, w.failOperation(ctx, operation.ID, err)
		}
		if err = w.db.SetRepositoryReviewBase(ctx, task.ID, repository.ID, input.ReviewBaseSHA); err != nil {
			return nil, w.failOperation(ctx, operation.ID, err)
		}
		if err = w.db.SetRepositoryBranch(ctx, task.ID, repository.ID, branch); err != nil {
			return nil, w.failOperation(ctx, operation.ID, err)
		}
		if err = w.completeOperation(ctx, operation.ID); err != nil {
			return nil, err
		}
		restored = append(restored, store.PhaseRepositoryInput{RepositoryID: repository.ID, ReviewBaseSHA: input.ReviewBaseSHA, HeadSHA: input.HeadSHA, BranchName: branch})
	}
	if phase.InputSnapshot == "" {
		return nil, fmt.Errorf("attempt input snapshot is required")
	}
	operation, err := w.beginOperation(ctx, task.ID, "", phase.ID, "restore_snapshot", phase.InputSnapshot)
	if err != nil {
		return nil, err
	}
	if err = w.MaterializeSnapshot(ctx, task, phase.InputSnapshot); err != nil {
		return nil, w.failOperation(ctx, operation.ID, err)
	}
	if err = w.completeOperation(ctx, operation.ID); err != nil {
		return nil, err
	}
	return restored, nil
}

func (w *workspaceService) Cleanup(ctx context.Context, task store.Task) error {
	if err := w.validateTaskWorkspace(task); err != nil {
		return err
	}
	operation, err := w.beginOperation(ctx, task.ID, "", "", "cleanup", task.WorkspacePath)
	if err != nil {
		return err
	}
	if w.sandbox != nil {
		cleanup := make([]CleanupRepository, 0, len(task.Repositories))
		for _, repository := range task.Repositories {
			cleanup = append(cleanup, CleanupRepository{RepositoryID: repository.ID, Name: repository.Name, SourceType: repository.SourceType, CanonicalPath: repository.CanonicalPath, WorkingPath: repository.WorkingPath})
		}
		w.gitMu.Lock()
		err = w.sandbox.Cleanup(ctx, CleanupRequest{TaskID: task.ID, WorkspaceRoot: task.WorkspacePath, Repositories: cleanup})
		w.gitMu.Unlock()
		if err != nil {
			return w.failOperation(ctx, operation.ID, err)
		}
	}
	if err = os.RemoveAll(task.WorkspacePath); err != nil {
		return w.failOperation(ctx, operation.ID, fmt.Errorf("remove task files: %w", err))
	}
	return w.completeOperation(ctx, operation.ID)
}

func (w *workspaceService) beginOperation(ctx context.Context, taskID, repositoryID, attemptID, kind string, request any) (store.WorkspaceOperation, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return store.WorkspaceOperation{}, fmt.Errorf("encode workspace operation: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	operation := store.WorkspaceOperation{ID: randomID(), TaskID: taskID, RepositoryID: repositoryID, AttemptID: attemptID, Kind: kind, Status: "planned", RequestJSON: string(encoded), CreatedAt: now, UpdatedAt: now}
	if err = w.db.CreateWorkspaceOperation(ctx, operation); err != nil {
		return store.WorkspaceOperation{}, err
	}
	if err = w.db.UpdateWorkspaceOperation(ctx, operation.ID, "running", ""); err != nil {
		return store.WorkspaceOperation{}, err
	}
	return operation, nil
}

func (w *workspaceService) completeOperation(ctx context.Context, id string) error {
	return w.db.UpdateWorkspaceOperation(context.WithoutCancel(ctx), id, "succeeded", "")
}

func (w *workspaceService) failOperation(ctx context.Context, id string, err error) error {
	if err == nil {
		return errors.New("workspace operation failed")
	}
	if updateErr := w.db.UpdateWorkspaceOperation(context.WithoutCancel(ctx), id, "failed", err.Error()); updateErr != nil {
		return fmt.Errorf("%w; record workspace failure: %v", err, updateErr)
	}
	return err
}

func (w *workspaceService) validateTaskWorkspace(task store.Task) error {
	expected, err := filepath.Abs(filepath.Join(w.root, "tasks", task.ID))
	if err != nil {
		return fmt.Errorf("task workspace is not owned by task")
	}
	actual, err := filepath.Abs(filepath.Clean(task.WorkspacePath))
	if err != nil || actual != expected {
		return fmt.Errorf("task workspace is not owned by task")
	}
	return nil
}

func (w *workspaceService) phaseInputs(ctx context.Context, phaseID string) ([]store.PhaseRepositoryInput, error) {
	reader, ok := w.db.(interface {
		PhaseRepositoryInputs(context.Context, string) ([]store.PhaseRepositoryInput, error)
	})
	if !ok {
		return nil, fmt.Errorf("workspace store cannot read phase inputs")
	}
	return reader.PhaseRepositoryInputs(ctx, phaseID)
}

func workspaceDirectories(root string) []string {
	return []string{root, filepath.Join(root, "workspace", "repositories"), filepath.Join(root, "attempts"), filepath.Join(root, "snapshots"), filepath.Join(root, "artifacts"), filepath.Join(root, "sessions"), filepath.Join(root, "workspace", "snapshots"), filepath.Join(root, "workspace", "branches"), filepath.Join(root, "workspace", "attempts"), filepath.Join(root, "repository-profiles"), filepath.Join(root, "prompts")}
}

func readMaterialization(path string) (Materialization, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Materialization{}, err
	}
	var profile Materialization
	if err = json.Unmarshal(body, &profile); err != nil {
		return Materialization{}, err
	}
	return profile, nil
}

func writeRepositoryProfile(taskWorkspace, name string, profile Materialization) error {
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repository profile: %w", err)
	}
	directory := filepath.Join(taskWorkspace, "repository-profiles")
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create repository profile directory: %w", err)
	}
	if err = os.WriteFile(filepath.Join(directory, name+".json"), encoded, 0o600); err != nil {
		return fmt.Errorf("write repository profile: %w", err)
	}
	return nil
}

func repositoryByID(repositories []store.TaskRepository, id string) (store.TaskRepository, bool) {
	for _, repository := range repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return store.TaskRepository{}, false
}

func retryBranch(taskID, key, repositoryID string) string {
	digest := sha256.Sum256([]byte(taskID + "\x00" + key + "\x00" + repositoryID))
	return "software-factory/retry/" + hex.EncodeToString(digest[:8])
}

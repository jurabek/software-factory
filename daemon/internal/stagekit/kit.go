package stagekit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// TaskConfig carries resolved task configuration for stage prompt rendering.
type TaskConfig struct {
	Config     config.Config
	ConfigPath string
	TaskDir    string
}

// Kit is the shared lifecycle support injected into every stage module. It
// owns durable mechanics: task locks, phase attempts, transitions,
// lineage, config resolution, events, and message draining.
type Kit struct {
	db         *store.DB
	harnesses  harness.Registry
	sandbox    workspace.Sandbox
	snapshots  *workspace.Service
	config     config.Config
	configPath string
	root       string
	locks      *Locker
}

// New constructs the shared stage kit.
func New(db *store.DB, harnesses harness.Registry, sandbox workspace.Sandbox, config config.Config, configPath, root string) *Kit {
	return &Kit{
		db:         db,
		harnesses:  harnesses,
		sandbox:    sandbox,
		snapshots:  workspace.New(db),
		config:     config,
		configPath: configPath,
		root:       root,
		locks:      &Locker{},
	}
}

// Locker hands out per-task mutexes. Stages lock only around begin and
// drain-then-publish, never during agent execution.
type Locker struct{ values sync.Map }

// Lock returns the mutex for a task.
func (l *Locker) Lock(id string) *sync.Mutex {
	value, _ := l.values.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// DB exposes the store for stage-owned reads.
func (k *Kit) DB() *store.DB { return k.db }

// AgentExec returns the shared agent-turn dependencies bound to this kit. When
// a registered harness exposes a native session reader, it is attached so
// native entries remain authoritative for usage and reports.
func (k *Kit) AgentExec() harness.Deps {
	return harness.Deps{DB: k.db, Harnesses: k.harnesses, NativeReader: k.nativeReader()}
}

func (k *Kit) nativeReader() harness.NativeReader {
	var reader harness.NativeReader
	for _, adapter := range k.harnesses {
		if candidate, ok := adapter.(harness.NativeReader); ok {
			reader = candidate
			break
		}
	}
	return reader
}

// MaterializeScratch materializes a snapshot digest into a scratch directory.
func (k *Kit) MaterializeScratch(ctx context.Context, task store.Task, digest, destination string) error {
	return k.snapshots.MaterializeScratch(ctx, task, digest, destination)
}

// CaptureSnapshot records the current task workspace.
func (k *Kit) CaptureSnapshot(ctx context.Context, task store.Task) (string, error) {
	snapshot, err := k.snapshots.CaptureSnapshot(ctx, task)
	if err != nil {
		return "", err
	}
	return snapshot.Digest, nil
}

// TaskDir returns the on-disk workspace for a task.
func (k *Kit) TaskDir(id string) string { return filepath.Join(k.root, "tasks", id) }

// Lock returns the task mutex.
func (k *Kit) Lock(id string) *sync.Mutex { return k.locks.Lock(id) }

// Transition advances a task if needed and records the reason.
func (k *Kit) Transition(ctx context.Context, task store.Task, to State, message string) error {
	if task.State == string(to) {
		return nil
	}
	return k.db.Transition(ctx, task.ID, task.State, string(to), task.ActivePhase, message)
}

// SetActiveStage records the active stage for a task.
func (k *Kit) SetActiveStage(ctx context.Context, taskID, stageID string) error {
	return k.db.SetActiveStage(ctx, taskID, stageID)
}

// Task loads a task.
func (k *Kit) Task(ctx context.Context, id string) (store.Task, error) { return k.db.Task(ctx, id) }

// TaskConfig resolves a task's frozen configuration so stages render prompts
// without the orchestrator.
func (k *Kit) TaskConfig(ctx context.Context, task store.Task) (TaskConfig, error) {
	configured, err := k.resolveConfig(task)
	if err != nil {
		return TaskConfig{}, err
	}
	return TaskConfig{Config: configured, ConfigPath: k.configPath, TaskDir: k.TaskDir(task.ID)}, nil
}

func (k *Kit) resolveConfig(task store.Task) (config.Config, error) {
	return config.Resolve(k.config, k.configPath, task.ConfigSnapshot)
}

// TaskPipeline resolves the selected pipeline for a task.
func (k *Kit) TaskPipeline(task store.Task) (config.Config, config.Pipeline, error) {
	return config.TaskPipeline(k.config, k.configPath, task.ConfigSnapshot, task.Pipeline)
}

// StageByKind returns the configured stage for a kind in the task's pipeline.
func (k *Kit) StageByKind(task store.Task, kind string) (config.Stage, error) {
	_, pipeline, err := k.TaskPipeline(task)
	if err != nil {
		return config.Stage{}, err
	}
	for _, stage := range pipeline.Stages {
		if stage.Kind == kind {
			return stage, nil
		}
	}
	return config.Stage{}, fmt.Errorf("task pipeline has no %s stage", kind)
}

// EnsurePrepared materializes the repository when a task has not been prepared.
func (k *Kit) EnsurePrepared(ctx context.Context, task store.Task) (store.Task, error) {
	if task.RepositoryPath != "" {
		return task, nil
	}
	phase, err := k.BeginPhase(ctx, task.ID, "creation", "creation", "factory", "Create and prepare repository")
	if err != nil {
		return store.Task{}, err
	}
	repositoryPath, err := k.prepareRepository(ctx, task)
	if err != nil {
		k.Fail(ctx, phase, err)
		return store.Task{}, err
	}
	if err = k.db.SetPrepared(ctx, task.ID, repositoryPath, task.ConfigSnapshot); err != nil {
		k.Fail(ctx, phase, err)
		return store.Task{}, err
	}
	if err = k.EndPhase(ctx, phase, "success", nil); err != nil {
		return store.Task{}, err
	}
	return k.db.Task(ctx, task.ID)
}

func (k *Kit) prepareRepository(ctx context.Context, task store.Task) (string, error) {
	if k.sandbox == nil {
		return "", fmt.Errorf("sandbox unavailable")
	}
	destination := filepath.Join(task.WorkspacePath, "workspace", "repository")
	profile, err := k.sandbox.Materialize(ctx, workspace.MaterializationRequest{TaskID: task.ID, SourceType: task.RepositoryType, Source: task.RepositorySource, Destination: destination})
	if err != nil {
		return "", err
	}
	if len(profile.Checks) == 0 {
		return "", fmt.Errorf("repository has no deterministic checks declared or detected")
	}
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode repository profile: %w", err)
	}
	if err = os.WriteFile(filepath.Join(task.WorkspacePath, "repository-profile.json"), encoded, 0o600); err != nil {
		return "", fmt.Errorf("write repository profile: %w", err)
	}
	_, err = k.db.ExecContext(ctx, `update tasks set canonical_repository_path=?,repository_path=?,base_sha=?,review_base_sha=?,branch_name=? where id=?`, profile.Root, destination, profile.BaseSHA, profile.BaseSHA, profile.BranchName, task.ID)
	if err != nil {
		return "", fmt.Errorf("save repository materialization: %w", err)
	}
	return destination, nil
}

// Sink opens a live-event sink for a stage turn.
func (k *Kit) Sink(taskID, phaseID, harnessName string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		switch payload := event.Payload.(type) {
		case session.ProcessStartPayload:
			if _, err := k.db.StartProcess(ctx, taskID, phaseID, harnessName, harnessName, payload.PID, payload.Command); err != nil {
				return err
			}
		case session.ProcessEndPayload:
			if err := k.db.EndProcess(ctx, taskID, payload.PID, payload.ExitCode); err != nil {
				return err
			}
		}
		return k.Trace(ctx, taskID, phaseID, event)
	}
}

// Trace appends a session entry with available-action metadata.
func (k *Kit) Trace(ctx context.Context, taskID, phaseID string, entry session.Entry) error {
	attemptID, branchID := phaseID, ""
	var actions []string
	if phaseID != "" {
		if phase, err := k.db.PhaseByID(ctx, taskID, phaseID); err == nil {
			attemptID = phase.ID
			branchID = phase.BranchID
			if task, taskErr := k.db.Task(ctx, taskID); taskErr == nil {
				actions = AvailableActions(&phase, task.State)
			} else {
				actions = AvailableActions(&phase, "")
			}
		}
	}
	_, err := k.db.AppendEvent(ctx, k.TaskDir(taskID), store.Event{ID: RandomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, BranchID: branchID, Kind: entry.Kind, Name: entry.Name, NativeEntryID: entry.NativeEntryID, RequestID: entry.RequestID, Payload: entry.Payload, Display: entry.Display, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

// AvailableActions returns server-computed actions for an attempt.
func AvailableActions(phase *store.Phase, taskState string) []string {
	actions := make([]string, 0, 3)
	switch taskState {
	case string(AwaitingApproval):
		actions = append(actions, "approve", "abort")
	case string(Paused):
		actions = append(actions, "resume", "abort")
	case string(Blocked):
		actions = append(actions, "resume", "abort")
	case string(Aborted), string(Completed):
	case string(Preparing), string(Planning), string(Building), string(Checking), string(Reviewing):
		actions = append(actions, "pause", "abort")
	}
	return actions
}

// IsReadOnlyOwner reports whether a stage owner must not modify the repository.
func IsReadOnlyOwner(owner string) bool { return owner == "planner" || owner == "reviewer" }

// PhaseReadOnly reports whether a phase must be read-only.
func PhaseReadOnly(phase store.Phase, role string) bool {
	return phase.Kind == "review" || IsReadOnlyOwner(role)
}

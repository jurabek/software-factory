package stagekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/attempt"
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
	db         *store.Store
	harnesses  harness.Registry
	sandbox    workspace.Sandbox
	snapshots  *workspace.Service
	config     config.Config
	configPath string
	root       string
	locks      *attempt.Locks
	attempts   attempt.Ledger
}

// New constructs the shared stage kit.
func New(db *store.Store, harnesses harness.Registry, sandbox workspace.Sandbox, config config.Config, configPath, root string, locks *attempt.Locks) *Kit {
	return &Kit{
		db:         db,
		harnesses:  harnesses,
		sandbox:    sandbox,
		snapshots:  workspace.New(db),
		config:     config,
		configPath: configPath,
		root:       root,
		locks:      locks,
		attempts:   attempt.NewLedger(db.Phases),
	}
}

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
func (k *Kit) Lock(id string) *sync.Mutex { return k.locks.For(id) }

// Transition advances a task if needed and records the reason.
func (k *Kit) Transition(ctx context.Context, task store.Task, to string, message string) error {
	if task.State == to {
		return nil
	}
	return k.db.Tasks.Transition(ctx, task.ID, task.State, to, task.ActivePhase, message)
}

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
	if err = k.db.Tasks.SetPrepared(ctx, task.ID, repositoryPath, task.ConfigSnapshot); err != nil {
		k.Fail(ctx, phase, err)
		return store.Task{}, err
	}
	if err = k.EndPhase(ctx, phase, "success", nil); err != nil {
		return store.Task{}, err
	}
	return k.db.Tasks.Get(ctx, task.ID)
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
	if err = k.db.Tasks.SetMaterialization(ctx, task.ID, profile.Root, destination, profile.BaseSHA, profile.BranchName); err != nil {
		return "", err
	}
	return destination, nil
}

// Sink opens a live-event sink for a stage turn.
func (k *Kit) Sink(taskID, phaseID, harnessName string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		switch payload := event.Payload.(type) {
		case session.ProcessStartPayload:
			if _, err := k.db.Processes.Start(ctx, taskID, phaseID, harnessName, harnessName, payload.PID, payload.Command); err != nil {
				return err
			}
		case session.ProcessEndPayload:
			if err := k.db.Processes.End(ctx, taskID, payload.PID, payload.ExitCode); err != nil {
				return err
			}
		}
		return k.Trace(ctx, taskID, phaseID, event)
	}
}

// Trace appends a session entry with available-action metadata.
func (k *Kit) Trace(ctx context.Context, taskID, phaseID string, entry session.Entry) error {
	attemptID := phaseID
	var actions []string
	if phaseID != "" {
		if phase, err := k.db.Phases.ByID(ctx, taskID, phaseID); err == nil {
			attemptID = phase.ID
			if task, taskErr := k.db.Tasks.Get(ctx, taskID); taskErr == nil {
				actions = AvailableActions(&phase, task.State)
			} else {
				actions = AvailableActions(&phase, "")
			}
		}
	}
	_, err := k.db.Events.Append(ctx, k.TaskDir(taskID), store.Event{ID: RandomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, Kind: entry.Kind, Name: entry.Name, NativeEntryID: entry.NativeEntryID, RequestID: entry.RequestID, Payload: entry.Payload, Display: entry.Display, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

// AvailableActions returns server-computed actions for an attempt.
func AvailableActions(phase *store.Phase, taskState string) []string {
	actions := make([]string, 0, 3)
	switch taskState {
	case AwaitingApproval:
		actions = append(actions, "approve", "abort")
	case Paused:
		actions = append(actions, "resume", "abort")
	case Blocked:
		actions = append(actions, "resume", "abort")
	case Aborted, Completed:
	case Preparing, Planning, Building, Checking, Reviewing:
		actions = append(actions, "pause", "abort")
	}
	return actions
}

// IsReadOnlyOwner reports whether a stage owner must not modify the repository.
func IsReadOnlyOwner(owner string) bool { return owner == "planner" || owner == "reviewer" }

// Upstream names an upstream stage attempt that must remain the current
// successful attempt of its stage kind.
type Upstream struct {
	Kind      string
	AttemptID string
}

// Entry describes how a stage begins: its kind, the upstream attempts it
// consumes, and the task state it runs in. EnterFrom restricts the transition
// to one source state; empty means any.
type Entry struct {
	Kind      string
	Requires  []Upstream
	Enter     string
	EnterFrom string
}

// AgentStage is an agent-driven stage reduced to its spec. The kit owns the
// lifecycle: resume from a saved attempt, begin or reuse the phase, render
// prompts, run the turn, deliver queued messages, enforce read-only, complete
// and fail. The stage supplies prompt data, validation, completion, and the
// mapping from a durable attempt to its typed result.
type AgentStage[T any] struct {
	Entry
	// After is the upstream attempt a saved result must postdate.
	After        string
	Role         string
	ReadOnly     bool
	Instructions string
	Data         func(ctx context.Context, task store.Task) (map[string]any, error)
	Validate     func(task store.Task, text string) (any, error)
	OnValid      func(ctx context.Context, task store.Task, phase store.Phase, payload string) error
	// Finalize decides the completion; Phase and From are filled by the kit.
	Finalize func(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult) (Completion, error)
	Result   func(task store.Task, phase store.Phase, payload string) (T, error)
}

// RunAgentStage resumes a durable result when present, otherwise runs the
// stage's agent turn and publishes the result.
func (k *Kit) RunAgentStage[T any](ctx context.Context, taskID string, spec AgentStage[T]) (T, error) {
	var zero T
	if result, ok, err := k.savedStage(ctx, taskID, spec); err != nil || ok {
		return result, err
	}
	task, phase, err := k.BeginStage(ctx, taskID, spec.Entry)
	if err != nil {
		return zero, err
	}
	readOnly := spec.ReadOnly || IsReadOnlyOwner(phase.Owner) || phase.Kind == "review"
	before := ""
	if readOnly {
		if before, err = workspace.Fingerprint(task); err != nil {
			k.Fail(ctx, phase, err)
			return zero, err
		}
	}
	turn, err := k.agentTurn(ctx, spec, task, phase, readOnly)
	if err != nil {
		k.Fail(ctx, phase, err)
		return zero, err
	}
	delivery := Delivery{Task: task, Phase: phase, Role: spec.Role, ReadOnly: readOnly, Instructions: spec.Instructions, Validate: func(text string) (any, error) { return spec.Validate(task, text) }}
	if spec.OnValid != nil {
		delivery.OnValid = func(ctx context.Context, payload string) error { return spec.OnValid(ctx, task, phase, payload) }
	}
	return k.DeliverAndFinalize(ctx, delivery, turn, func(turn harness.TurnResult) (T, error) {
		return k.finalizeStage(ctx, spec, task, phase, turn, readOnly, before)
	})
}

// BeginStage verifies upstream attempts are still current, marks the stage
// active, enters its task state, and begins or reuses its phase.
func (k *Kit) BeginStage(ctx context.Context, taskID string, entry Entry) (store.Task, store.Phase, error) {
	task, err := k.db.Tasks.Get(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = k.requireUpstream(ctx, task, entry.Requires); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stageDef, err := k.StageByKind(task, entry.Kind)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = k.db.Tasks.SetActiveStage(ctx, task.ID, stageDef.ID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task, err = k.db.Tasks.Get(ctx, task.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if entry.EnterFrom == "" || task.State == entry.EnterFrom {
		if err = k.Transition(ctx, task, entry.Enter, ""); err != nil {
			return store.Task{}, store.Phase{}, err
		}
		task.State = entry.Enter
	}
	phase, err := k.BeginOrReusePhase(ctx, task.ID, stageDef.ID, stageDef.Kind, stageDef.Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

func (k *Kit) requireUpstream(ctx context.Context, task store.Task, requires []Upstream) error {
	for _, upstream := range requires {
		stageDef, err := k.StageByKind(task, upstream.Kind)
		if err != nil {
			return err
		}
		if err = k.attempts.Require(ctx, task.ID, stageDef.ID, upstream.AttemptID); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kit) savedStage[T any](ctx context.Context, taskID string, spec AgentStage[T]) (T, bool, error) {
	var zero T
	task, err := k.db.Tasks.Get(ctx, taskID)
	if err != nil {
		return zero, false, err
	}
	if err = k.requireUpstream(ctx, task, spec.Requires); err != nil {
		return zero, false, err
	}
	stageDef, err := k.StageByKind(task, spec.Kind)
	if err != nil {
		return zero, false, err
	}
	queued, err := k.db.Messages.QueuedForStages(ctx, task.ID, stageDef.ID, stageDef.Agent)
	if err != nil || queued {
		return zero, false, err
	}
	phase, ok, err := k.attempts.Resume(ctx, task.ID, stageDef.ID, spec.After)
	if err != nil || !ok {
		return zero, false, err
	}
	result, err := k.stageResult(ctx, spec, task, phase)
	if err != nil {
		return zero, false, err
	}
	return result, true, nil
}

func (k *Kit) stageResult[T any](ctx context.Context, spec AgentStage[T], task store.Task, phase store.Phase) (T, error) {
	payload, err := k.PhaseEnvelope(ctx, task.ID, phase.ID)
	if err != nil {
		var zero T
		return zero, err
	}
	return spec.Result(task, phase, payload)
}

func (k *Kit) agentTurn[T any](ctx context.Context, spec AgentStage[T], task store.Task, phase store.Phase, readOnly bool) (harness.TurnResult, error) {
	configured, err := k.TaskConfig(ctx, task)
	if err != nil {
		return harness.TurnResult{}, err
	}
	agent, ok := configured.Config.Agent(phase.Owner)
	if !ok {
		return harness.TurnResult{}, fmt.Errorf("agent %s not configured", phase.Owner)
	}
	data, err := spec.Data(ctx, task)
	if err != nil {
		return harness.TurnResult{}, err
	}
	systemPrompt, userPrompt, err := RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		spec.Instructions,
	)
	if err != nil {
		return harness.TurnResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := k.AgentExec()
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	return harness.RunTurn(ctx, turner, harness.TurnInput{
		TaskID: task.ID, RequestID: RandomID(), Phase: phase, Role: spec.Role,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath:     task.RepositoryPath,
		SessionDir:   filepath.Join(configured.TaskDir, "sessions", phase.Name, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: readOnly, EnvelopeKind: phaseEnvelopeKind(phase, spec.Role), CorrectionSuffix: spec.Instructions,
		Validate: func(text string) (any, error) { return spec.Validate(task, text) },
		Sink:     k.Sink(task.ID, phase.ID, harnessName),
	})
}

func (k *Kit) finalizeStage[T any](ctx context.Context, spec AgentStage[T], task store.Task, phase store.Phase, turn harness.TurnResult, readOnly bool, before string) (T, error) {
	var zero T
	if readOnly {
		after, err := workspace.Fingerprint(task)
		if err == nil && before != after {
			err = fmt.Errorf("%s modified repository", phase.Name)
		}
		if err != nil {
			k.Fail(ctx, phase, err)
			return zero, err
		}
	}
	completion, err := spec.Finalize(ctx, task, phase, turn)
	if err != nil {
		k.Fail(ctx, phase, err)
		return zero, err
	}
	completion.Phase, completion.From = phase, spec.Enter
	if err = k.Complete(ctx, completion); err != nil {
		k.Fail(ctx, phase, err)
		return zero, err
	}
	task, err = k.db.Tasks.Get(ctx, task.ID)
	if err != nil {
		return zero, err
	}
	phase, err = k.db.Phases.ByID(ctx, task.ID, phase.ID)
	if err != nil {
		return zero, err
	}
	return k.stageResult(ctx, spec, task, phase)
}

func (k *Kit) DeliverAndFinalize[T any](ctx context.Context, spec Delivery, turn harness.TurnResult, finalize func(harness.TurnResult) (T, error)) (T, error) {
	var zero T
	for {
		latest, err := k.deliver(ctx, spec)
		if err != nil {
			k.Fail(ctx, spec.Phase, err)
			return zero, err
		}
		if latest.Payload != "" {
			turn = latest
		}
		lock := k.Lock(spec.Task.ID)
		lock.Lock()
		_, err = k.db.Messages.NextQueued(ctx, spec.Task.ID, spec.Phase.Name)
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			k.Fail(ctx, spec.Phase, err)
			return zero,
				err
		}
		result, finalizeErr := finalize(turn)
		lock.Unlock()
		return result,
			finalizeErr
	}
}

func (k *Kit) deliver(ctx context.Context, spec Delivery) (harness.TurnResult, error) {
	task, phase := spec.Task, spec.Phase
	configured, err := k.resolveConfig(task)
	if err != nil {
		return harness.TurnResult{}, err
	}
	agent, ok := configured.Agent(
		phase.Owner)
	if !ok {
		return harness.TurnResult{}, fmt.Errorf("agent %s not configured",
			phase.Owner)
	}
	storedSession, err := k.db.AgentSessions.Get(ctx, task.ID,
		phase.Name)
	if err != nil {
		return harness.TurnResult{}, err
	}
	var latest harness.TurnResult
	for {
		message, nextErr := k.db.Messages.NextQueued(ctx, task.ID, phase.Name)
		if errors.Is(
			nextErr, store.ErrNotFound) {
			return latest, nil
		}
		if nextErr != nil {
			return harness.TurnResult{}, nextErr
		}
		if message.AgentSessionID != storedSession.HarnessSessionID {
			err = fmt.Errorf("message agent session identity is stale")
			k.failMessage(ctx, message, phase, "session_unavailable")
			return harness.TurnResult{}, err
		}
		systemPrompt, promptErr := k.messageSystemPrompt(ctx, message, spec.Role, spec.Instructions)
		if promptErr != nil {
			k.failMessage(ctx, message, phase, "context_unavailable")
			return harness.TurnResult{}, promptErr
		}
		turner := k.AgentExec()
		turner.AgentDeadlineMS = configured.Runtime.AgentDeadlineMS
		turner.JSONFixAttempts = configured.Runtime.JSONFixAttempts
		turn, runErr := harness.RunTurn(ctx, turner, harness.TurnInput{
			TaskID: task.ID, RequestID: RandomID(), Phase: phase, Role: spec.Role,
			HarnessName: configured.Defaults.CodingAgent, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color, RepoPath: task.RepositoryPath, SessionDir: storedSession.SessionDirectory, SystemPrompt: systemPrompt, UserPrompt: message.Text,
			ReadOnly: spec.ReadOnly, EnvelopeKind: phaseEnvelopeKind(phase, spec.Role), CorrectionSuffix: spec.Instructions, Validate: spec.Validate, Sink: k.Sink(task.ID, phase.ID, storedSession.Harness), OnDispatch: k.markDelivered(message, &phase),
		})
		if runErr != nil {
			reason := "harness_error"
			if errors.Is(runErr, context.Canceled) {
				reason = "invocation_cancelled"
			}
			k.failMessage(ctx, message, phase, reason)
			return harness.TurnResult{}, runErr
		}
		if spec.OnValid != nil {
			if evidenceErr := spec.OnValid(ctx, turn.Payload); evidenceErr != nil {
				k.failMessage(ctx, message,
					phase, "evidence_persistence_failed",
				)
				return harness.TurnResult{}, evidenceErr
			}
		}
		latest = turn
	}
}

func (k *Kit) markDelivered(message store.Message, phase *store.Phase) func(context.Context, string) error {
	delivered := false
	return func(dispatchCtx context.Context, _ string) error {
		if delivered {
			return nil
		}
		delivered = true
		message.DeliveryStatus = "delivered"
		message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
		event, err := MessageEvent(dispatchCtx,
			k.db, message,
			phase)
		if err != nil {
			return err
		}
		return k.db.Messages.DeliverWithEvent(dispatchCtx,
			message.TaskID, message.ID, event,
			k.TaskDir(message.TaskID))
	}
}

func (k *Kit) messageSystemPrompt(ctx context.Context, message store.Message, role, instructions string) (string, error) {
	contextValue := map[string]any{}
	if message.Target != nil {
		contextValue["target"] = message.Target
	}
	checks, err := k.db.Checks.List(
		ctx, message.TaskID)
	if err != nil {
		return "", err
	}
	failed := make([]store.Check, 0)
	for _, check := range checks {
		if check.Status == "failed" {
			failed = append(failed, check)
		}
	}
	if len(failed) > 0 {
		contextValue["failed_checks"] = failed
	}
	if len(contextValue) == 0 {
		return instructions,
			nil
	}
	encoded, err := json.Marshal(contextValue)
	if err != nil {
		return "", fmt.Errorf("encode message context: %w",
			err)
	}
	return instructions + "\n\nFactory context for this turn: " + string(encoded), nil
}

func (k *Kit) failMessage(ctx context.Context, message store.Message, phase store.Phase, reason string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message.DeliveryStatus = "failed"
	message.FailureReason = reason
	event, err := MessageEvent(cleanupCtx, k.db, message, &phase)
	if err != nil {
		return
	}
	_, _ = k.db.Messages.FailWithEvent(cleanupCtx, message.TaskID, message.ID, reason, event, k.TaskDir(message.TaskID))
}

func (k *Kit) BeginPhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	phases, err := k.db.Phases.List(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
	task, _ := k.db.Tasks.Get(ctx, taskID)
	definitionID := k.ensureDefinition(ctx, taskID, name, kind, owner)
	inputSnapshot := ""
	if IsReadOnlyOwner(owner) && len(phases) > 0 {
		inputSnapshot = phases[len(phases)-1].OutputSnapshot
	}
	if inputSnapshot == "" {
		if snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, store.Task{ID: taskID, WorkspacePath: k.TaskDir(taskID)}); captureErr == nil {
			inputSnapshot = snapshot.Digest
		}
	}
	phase := store.Phase{
		ID: RandomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description, Status: "running", Attempt: 1, DefinitionID: definitionID, InputSnapshot: inputSnapshot,
	}
	event := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: name, Owner: owner, Kind: kind, InputSnapshot: inputSnapshot})
	eventValue := store.Event{
		ID: RandomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC(),
	}
	if err = k.db.Phases.StartWithEvent(ctx, k.TaskDir(taskID), phase, task.State, eventValue); err != nil {
		return store.Phase{}, err
	}
	return phase,
		nil
}

func (k *Kit) BeginOrReusePhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	if phase,
		ok, err := k.pendingStagePhase(ctx, taskID, name); err != nil {
		return store.Phase{}, err
	} else if ok {
		if phase.Status == "queued" {
			if err = k.db.Phases.StartQueued(ctx, taskID, phase.ID); err != nil {
				return store.Phase{},
					err
			}
			phase.Status = "running"
		}
		entry := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, InputSnapshot: phase.InputSnapshot})
		if err = k.Trace(ctx, taskID, phase.ID, entry); err != nil {
			return store.Phase{}, err
		}
		return phase, nil
	}
	return k.BeginPhase(ctx, taskID, name, kind, owner, description)
}

func (k *Kit) pendingStagePhase(ctx context.Context, taskID, name string) (store.Phase, bool, error) {
	phases, err := k.db.Phases.List(ctx,
		taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {
		if phase.Name != name || phase.Superseded {
			continue
		}
		if phase.Status == "queued" || phase.Status == "running" {
			return phase, true, nil
		}
		return store.Phase{}, false, nil
	}
	return store.Phase{}, false, nil
}

func (k *Kit) ensureDefinition(ctx context.Context, taskID, key, executor, owner string) string {
	existing, err := k.db.Definitions.Latest(ctx, taskID, key)
	if err == nil {
		return existing.ID
	}
	definition := store.PhaseDefinition{
		ID: RandomID(), TaskID: taskID, PhaseKey: key, Revision: 1, Executor: executor, Owner: owner, Spec: "{}",
	}
	definition.Digest = PlanDigest(key, 1, "{}")
	definition.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = k.db.Definitions.Create(ctx, definition); err != nil {
		return ""
	}
	return definition.ID
}

func (k *Kit) EndPhase(ctx context.Context, phase store.Phase, status string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	outputSnapshot := phase.InputSnapshot
	if status != "success" || !IsReadOnlyOwner(phase.Owner) {
		if task, taskErr := k.db.Tasks.Get(ctx, phase.TaskID); taskErr == nil {
			if snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, task); captureErr == nil {
				if status == "success" && (phase.Kind == "agent" || phase.Kind == "build" || phase.Kind == "review" || phase.Kind == "check" || phase.Kind == "git") {
					outputSnapshot = snapshot.Digest
				} else if status != "success" {
					outputSnapshot = snapshot.Digest
				}
			}
		}
	}
	if outputSnapshot != "" && outputSnapshot != phase.InputSnapshot {
		_ = k.db.Phases.SetOutputSnapshot(context.Background(), phase.ID, outputSnapshot)
		phase.OutputSnapshot = outputSnapshot
	} else if phase.InputSnapshot != "" {
		_ = k.db.Phases.SetOutputSnapshot(context.Background(), phase.ID, phase.InputSnapshot)
		phase.OutputSnapshot = phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: phase.ID, Name: phase.Name,
		Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot,
	})
	return k.db.Phases.EndWithEvent(ctx, k.TaskDir(phase.TaskID), phase.ID, status, message, phase.OutputSnapshot, store.Event{ID: RandomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, StartedAt: time.Now().UTC()})
}

func (k *Kit) Fail(ctx context.Context, phase store.Phase, cause error) {
	_ = k.EndPhase(context.Background(), phase, "failed", cause)
}

func (k *Kit) Complete(ctx context.Context, c Completion) error {
	message := ""
	if c.Cause != nil {
		message = c.Cause.Error()
	}
	task, err := k.db.Tasks.Get(ctx, c.Phase.TaskID)
	if err != nil {
		return err
	}
	if c.Status == "success" {
		snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, task)
		if captureErr != nil {
			return fmt.Errorf("capture phase output snapshot: %w", captureErr)
		}
		c.Phase.OutputSnapshot = snapshot.Digest
	} else if c.Phase.OutputSnapshot == "" {
		c.Phase.OutputSnapshot = c.Phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: c.Phase.ID, Name: c.Phase.Name, Owner: c.Phase.Owner, Kind: c.Phase.Kind, Status: c.Status, Error: message, InputSnapshot: c.Phase.InputSnapshot, OutputSnapshot: c.Phase.OutputSnapshot,
	})
	eventValue := store.Event{
		ID: RandomID(), TaskID: c.Phase.TaskID,
		PhaseID: c.Phase.ID, AttemptID: c.Phase.ID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&c.Phase, task.State),
		StartedAt:        time.Now().UTC(),
	}
	dir := k.TaskDir(c.Phase.TaskID)
	return k.db.Phases.Complete(ctx, dir, store.PhaseCompletion{
		PhaseID: c.Phase.ID, TaskID: c.Phase.TaskID, From: c.From, To: c.To, Status: c.Status, Message: message,
		OutputSnapshot: c.Phase.OutputSnapshot, Approval: c.Approval, Checks: c.Checks, Comparisons: c.Comparisons, Event: eventValue,
	})
}

// Attempts returns the attempt ledger for stages that resolve resume state
// without the agent lifecycle.
func (k *Kit) Attempts() attempt.Ledger { return k.attempts }

func (k *Kit) PhaseEnvelope(ctx context.Context, taskID, phaseID string) (string, error) {
	envelopes, err := k.db.Envelopes.List(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, envelope := range slices.Backward(envelopes) {
		if envelope.PhaseID == phaseID && envelope.Valid {
			return envelope.Payload, nil
		}
	}
	return "", store.ErrNotFound
}

// ValidEnvelope returns the latest valid envelope a stage produced.
func (k *Kit) ValidEnvelope(ctx context.Context, taskID, stageID string) (string, error) {
	return k.db.Envelopes.Valid(ctx, taskID, stageID)
}

// ApprovePlan records plan approval and its history event.
func (k *Kit) ApprovePlan(ctx context.Context, taskID, digest, actor string) error {
	event := store.Event{
		ID: RandomID(), TaskID: taskID, Kind: session.KindCustom, Name: "task_approved", Payload: session.CustomPayload{
			CustomType: "task_approved",
			Data:       session.BoundedJSON(map[string]any{"task_id": taskID, "plan_digest": digest, "actor": actor}),
		}, Display: session.Display{
			Role:   "system",
			Status: "success", Title: "Plan approved",
		}, AvailableActions: []string{"pause", "abort"}, StartedAt: time.Now().UTC(),
	}
	if err := k.db.Tasks.SetApproval(ctx, taskID, digest, actor); err != nil {
		return err
	}
	_, err := k.db.Events.Append(ctx, k.TaskDir(taskID), event)
	return err
}

// Checks returns every recorded check for a task.
func (k *Kit) Checks(ctx context.Context, taskID string) ([]store.Check, error) {
	return k.db.Checks.List(ctx, taskID)
}

// SaveCheck records one check observation.
func (k *Kit) SaveCheck(ctx context.Context, check store.Check) error {
	return k.db.Checks.Save(ctx, check)
}

// Comparisons returns every recorded baseline comparison for a task.
func (k *Kit) Comparisons(ctx context.Context, taskID string) ([]store.Comparison, error) {
	return k.db.Evidence.Comparisons(ctx, taskID)
}

// SaveComparison records one baseline comparison.
func (k *Kit) SaveComparison(ctx context.Context, comparison store.Comparison) error {
	return k.db.Evidence.SaveComparison(ctx, comparison)
}

// TestChanges returns the recorded test evidence for a task.
func (k *Kit) TestChanges(ctx context.Context, taskID string) ([]store.TestChange, error) {
	return k.db.Evidence.TestChanges(ctx, taskID)
}

// SaveTestChanges records test evidence for an attempt.
func (k *Kit) SaveTestChanges(ctx context.Context, changes []store.TestChange) error {
	return k.db.Evidence.SaveTestChanges(ctx, changes)
}

// StartProcess records a factory-owned process for a phase.
func (k *Kit) StartProcess(ctx context.Context, taskID, phaseID, kind, name string, pid int, command string) error {
	_, err := k.db.Processes.Start(ctx, taskID, phaseID, kind, name, pid, command)
	return err
}

// EndProcess records a factory-owned process exit.
func (k *Kit) EndProcess(ctx context.Context, taskID string, pid, exitCode int) error {
	return k.db.Processes.End(ctx, taskID, pid, exitCode)
}

// BuildBaseline returns the input snapshot of the latest current build attempt.
func (k *Kit) BuildBaseline(ctx context.Context, taskID string) (string, error) {
	phases, err := k.db.Phases.List(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, candidate := range slices.Backward(phases) {
		if candidate.Kind == "build" && candidate.InputSnapshot != "" && !candidate.Superseded {
			return candidate.InputSnapshot, nil
		}
	}
	return "", nil
}

// ErrStageBudgetExceeded reports that a stage exhausted its retry budget.
var ErrStageBudgetExceeded = errors.New("stage_retry_budget_exceeded")

// EnforceStageBudget refuses further retries once a stage accumulates three
// failed phases, failing any queued stage messages so callers observe the
// budget decision instead of dispatching another attempt.
func (k *Kit) EnforceStageBudget(ctx context.Context, taskID, stageID, agent string) error {
	count, err := k.db.Phases.FailedCount(ctx, taskID, stageID)
	if err != nil {
		return err
	}
	if count < 3 {
		return nil
	}
	_ = k.db.Messages.FailQueuedForStages(ctx, taskID, ErrStageBudgetExceeded.Error(), stageID, agent)
	return ErrStageBudgetExceeded
}

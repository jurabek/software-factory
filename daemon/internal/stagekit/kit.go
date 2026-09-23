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

func (k *Kit) DeliverAndFinalize[T any](ctx context.
	Context, spec Delivery, turn harness.TurnResult, finalize func(harness.TurnResult) (T, error),
) (T, error) {
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
		lock.
			Lock()
		_, err = k.db.NextQueuedMessage(ctx, spec.Task.ID, spec.Phase.Name)
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
	if err !=
		nil {
		return harness.TurnResult{}, err
	}
	agent, ok := configured.Agent(
		phase.Owner)
	if !ok {
		return harness.TurnResult{}, fmt.Errorf("agent %s not configured",
			phase.
				Owner)
	}
	storedSession,
		err := k.db.AgentSession(ctx, task.ID,
		phase.Name)
	if err !=
		nil {
		return harness.TurnResult{}, err
	}
	var latest harness.TurnResult
	for {
		message, nextErr := k.db.NextQueuedMessage(ctx, task.ID, phase.Name)
		if errors.Is(
			nextErr,

			store.ErrNotFound) {
			return latest, nil
		}
		if nextErr != nil {
			return harness.TurnResult{}, nextErr
		}
		if message.AgentSessionID != storedSession.
			HarnessSessionID {
			err = fmt.Errorf("message agent session identity is stale")
			k.failMessage(ctx, message, phase, "session_unavailable")
			return harness.TurnResult{}, err
		}
		systemPrompt, promptErr := k.messageSystemPrompt(ctx, message, spec.Role, spec.Instructions)
		if promptErr != nil {
			k.failMessage(ctx, message, phase, "context_unavailable")
			return harness.
				TurnResult{}, promptErr
		}
		turner := k.AgentExec()
		turner.AgentDeadlineMS = configured.Runtime.AgentDeadlineMS
		turner.JSONFixAttempts = configured.Runtime.
			JSONFixAttempts
		turn, runErr := harness.RunTurn(ctx, turner, harness.TurnInput{
			TaskID: task.ID, RequestID: RandomID(), Phase: phase, Role: spec.Role,
			HarnessName: configured.Defaults.CodingAgent, Model: agent.Model, Thinking: agent.
					Thinking, Color:                agent.Color, RepoPath: task.RepositoryPath, SessionDir: storedSession.
					SessionDirectory, SystemPrompt: systemPrompt, UserPrompt: message.Text,
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
			if evidenceErr := spec.OnValid(ctx, turn.
				Payload); evidenceErr !=
				nil {
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
		event,
			err := MessageEvent(dispatchCtx,
			k.db, message,
			phase)
		if err != nil {
			return err
		}
		return k.db.DeliverMessageWithEvent(dispatchCtx,
			message.TaskID, message.ID, event,
			k.TaskDir(message.TaskID))
	}
}

func (k *Kit) messageSystemPrompt(ctx context.
	Context,
	message store.Message, role, instructions string,
) (string, error) {
	contextValue := map[string]any{}
	if message.Target != nil {
		contextValue["target"] = message.Target
	}
	checks, err := k.db.Checks(
		ctx,

		message.TaskID)
	if err != nil {
		return "", err
	}
	failed := make([]store.
		Check, 0)
	for _, check := range checks {
		if check.Status == "failed" {
			failed = append(failed, check)
		}
	}
	if len(failed) > 0 {
		contextValue["failed_checks"] = failed
	}
	if len(contextValue) ==
		0 {
		return instructions,
			nil
	}
	encoded, err := json.Marshal(contextValue)
	if err != nil {
		return "", fmt.
			Errorf("encode message context: %w",
				err)
	}
	return instructions +
		"\n\nFactory context for this turn: " + string(encoded), nil
}

func (k *Kit) failMessage(ctx context.Context, message store.Message,

	phase store.Phase, reason string,
) {
	cleanupCtx, cancel := context.
		WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message.DeliveryStatus = "failed"
	message.FailureReason = reason
	event, err := MessageEvent(cleanupCtx, k.db, message, &phase)
	if err != nil {
		return
	}
	_, _ = k.db.FailMessageWithEvent(cleanupCtx, message.
		TaskID, message.ID, reason, event, k.TaskDir(message.TaskID))
}

func (k *Kit) PhaseByID(ctx context.
	Context, taskID, phaseID string,
) (store.Phase, error) {
	return k.
		db.PhaseByID(ctx, taskID, phaseID)
}

func (
	k *Kit) BeginPhase(ctx context.Context, taskID, name, kind, owner,
	description string,
) (store.Phase, error) {
	phases, err := k.
		db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{},

			err
	}
	task, _ := k.db.Task(ctx, taskID)
	definitionID := k.ensureDefinition(ctx, taskID, name, kind, owner)
	inputSnapshot := ""
	if IsReadOnlyOwner(owner) && len(phases) > 0 {
		inputSnapshot = phases[len(phases)-1].OutputSnapshot
	}
	if inputSnapshot == "" {
		if snapshot, captureErr := k.snapshots.
			CaptureSnapshot(ctx, store.
				Task{ID: taskID, WorkspacePath: k.TaskDir(taskID)}); captureErr ==
			nil {
			inputSnapshot = snapshot.Digest
		}
	}
	phase := store.Phase{
		ID: RandomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description,

		Status: "running", Attempt: 1, BranchID: task.SelectedBranchID, DefinitionID: definitionID, InputSnapshot: inputSnapshot,
	}
	event := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: name, Owner: owner, Kind: kind, InputSnapshot: inputSnapshot})
	eventValue := store.Event{
		ID: RandomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC(),
	}
	if err = k.db.StartPhaseWithEvent(ctx, k.TaskDir(taskID), phase, task.
		State, eventValue); err != nil {
		return store.Phase{}, err
	}
	return phase,
		nil
}

func (k *Kit) BeginOrReusePhase(ctx context.Context, taskID, name, kind,
	owner, description string,
) (store.Phase, error) {
	if phase,
		ok,

		err := k.pendingStagePhase(ctx, taskID, name); err != nil {
		return store.
			Phase{}, err
	} else if ok {
		if phase.Status == "queued" {
			if err = k.db.StartQueuedPhase(ctx, taskID, phase.ID); err != nil {
				return store.Phase{},
					err
			}
			phase.Status = "running"
		}
		entry := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: phase.
			Name, Owner: phase.
			Owner, Kind: phase.
			Kind, InputSnapshot: phase.InputSnapshot})
		if err = k.Trace(ctx, taskID, phase.ID, entry); err != nil {
			return store.Phase{}, err
		}
		return phase, nil
	}
	return k.BeginPhase(ctx, taskID,

		name, kind, owner, description)
}

func (k *Kit) pendingStagePhase(ctx context.Context, taskID,
	name string,
) (store.Phase, bool, error) {
	phases, err := k.db.Phases(ctx,
		taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {
		if phase.
			Name !=
			name || phase.Superseded {
			continue
		}
		if phase.Status == "queued" ||
			phase.Status == "running" {
			return phase, true, nil
		}
		return store.
			Phase{}, false, nil
	}
	return store.Phase{}, false, nil
}

func (k *Kit) ensureDefinition(ctx context.Context, taskID,
	key,
	executor,

	owner string,
) string {
	existing, err := k.db.LatestDefinition(ctx, taskID, key)
	if err == nil {
		return existing.ID
	}
	definition := store.PhaseDefinition{
		ID: RandomID(), TaskID: taskID,

		PhaseKey: key, Revision: 1, Executor: executor, Owner: owner, Spec: "{}",
	}
	definition.Digest = PlanDigest(key, 1, "{}")
	definition.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = k.db.CreateDefinition(ctx, definition); err != nil {
		return ""
	}
	return definition.
		ID
}

func (k *Kit) EndPhase(ctx context.Context, phase store.
	Phase, status string, cause error,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	outputSnapshot := phase.InputSnapshot
	if status != "success" || !IsReadOnlyOwner(phase.Owner) {
		if task, taskErr := k.db.Task(ctx, phase.TaskID); taskErr == nil {
			if snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, task); captureErr ==
				nil {
				if status == "success" && (phase.Kind ==
					"agent" || phase.Kind ==
					"build" || phase.Kind ==
					"review" || phase.Kind ==
					"check" || phase.
					Kind == "git") {
					outputSnapshot = snapshot.Digest
				} else if status !=
					"success" {
					outputSnapshot = snapshot.Digest
				}
			}
		}
	}
	if outputSnapshot !=
		"" && outputSnapshot != phase.InputSnapshot {
		_, _ = k.db.ExecContext(context.Background(), `update phases set output_snapshot=? where id=?`,
			outputSnapshot, phase.ID)
		phase.OutputSnapshot = outputSnapshot
	} else if phase.InputSnapshot != "" {
		_, _ = k.db.ExecContext(context.
			Background(), `update phases set output_snapshot=? where id=?`,

			phase.InputSnapshot, phase.ID)
		phase.OutputSnapshot = phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: phase.ID, Name: phase.Name,
		Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot,
	})
	return k.db.
		EndPhaseWithEvent(ctx, k.TaskDir(phase.TaskID), phase.ID, status, message, phase.
			OutputSnapshot, store.Event{ID: RandomID(), TaskID: phase.TaskID, PhaseID: phase.
			ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.
			Name, Payload: event.Payload, Display: event.Display, StartedAt: time.Now().UTC()})
}

func (k *Kit) Fail(
	ctx context.Context, phase store.Phase, cause error,
) {
	_ = k.EndPhase(context.Background(), phase, "failed", cause)
}

func (k *Kit) Complete(ctx context.Context, c Completion) error {
	message := ""
	if c.Cause != nil {
		message = c.Cause.Error()
	}
	task, err := k.db.Task(ctx, c.Phase.TaskID)
	if err !=
		nil {
		return err
	}
	if c.Status == "success" {
		snapshot, captureErr := k.
			snapshots.CaptureSnapshot(ctx, task)
		if captureErr != nil {
			return fmt.Errorf("capture phase output snapshot: %w", captureErr)
		}
		c.Phase.OutputSnapshot = snapshot.Digest
	} else if c.Phase.OutputSnapshot ==
		"" {
		c.Phase.
			OutputSnapshot = c.Phase.
			InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: c.Phase.
			ID, Name:    c.Phase.Name, Owner: c.Phase.
			Owner, Kind: c.Phase.Kind, Status: c.Status, Error: message, InputSnapshot: c.Phase.InputSnapshot, OutputSnapshot: c.Phase.OutputSnapshot,
	})
	eventValue := store.Event{
		ID: RandomID(), TaskID: c.Phase.TaskID,
		PhaseID: c.Phase.ID, AttemptID: c.Phase.ID, BranchID: c.Phase.BranchID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&c.Phase, task.State),
		StartedAt:        time.Now().UTC(),
	}
	dir := k.TaskDir(c.Phase.TaskID)
	if c.Planner {
		return k.db.CompletePlannerPhaseWithApproval(ctx, dir, c.Phase.ID, c.Phase.TaskID,
			string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, c.Approval,
			eventValue)
	}
	if len(c.Checks) > 0 || len(c.Comparisons) > 0 {
		return k.db.CompleteVerificationPhaseWithEvidenceAndEvent(ctx, dir, c.Phase.ID, c.Phase.
			TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot,
			c.Checks, c.Comparisons, eventValue)
	}
	return k.db.CompletePhaseWithTransitionAndEvent(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.
		Status, message, c.Phase.OutputSnapshot, eventValue)
}

func (k *Kit) LatestStageAttempt(ctx context.Context, taskID,
	stageID string,
) (store.Phase, bool, error) {
	phases, err := k.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {
		if phase.
			Name == stageID && !phase.Superseded {
			return phase, true, nil
		}
	}
	return store.Phase{}, false, nil
}

func (k *Kit) SuccessfulPhase(ctx context.Context, taskID, name string) (store.Phase, bool, error) {
	phase, ok, err := k.LatestStageAttempt(ctx, taskID, name)
	if err != nil || !ok || phase.
		Status != "success" {
		return store.Phase{}, false, err
	}
	return phase, true,
		nil
}

func (k *Kit) RequireAttempt(ctx context.Context, taskID,
	name, attemptID string,
) error {
	phase, ok, err := k.SuccessfulPhase(ctx, taskID, name)
	if err != nil {
		return err
	}
	if !ok || phase.ID != attemptID {
		return fmt.Errorf("%s result %s is no longer eligible",
			name, attemptID)
	}
	return nil
}

func (k *Kit) AttemptAfter(ctx context.Context, taskID, attemptID,
	upstreamID string,
) (bool, error) {
	attempt, err := k.
		db.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return false,
			err
	}
	upstream, err := k.db.PhaseByID(ctx, taskID, upstreamID)
	if err !=
		nil {
		return false, err
	}
	return EligibleAfter(attempt.Sequence, upstream.
		Sequence), nil
}

func (
	k *Kit) PhaseEnvelope(ctx context.Context, taskID, phaseID string) (string, error) {
	envelopes, err := k.db.Envelopes(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, envelope := range slices.Backward(envelopes) {
		if envelope.PhaseID == phaseID && envelope.
			Valid {
			return envelope.Payload, nil
		}
	}
	return "", store.ErrNotFound
}

// ErrStageBudgetExceeded reports that a stage exhausted its retry budget.
var ErrStageBudgetExceeded = errors.New("stage_retry_budget_exceeded")

// EnforceStageBudget refuses further retries once a stage accumulates three
// failed phases, failing any queued stage messages so callers observe the
// budget decision instead of dispatching another attempt.
func (k *Kit) EnforceStageBudget(ctx context.Context, taskID, stageID, agent string) error {
	count, err := k.db.FailedPhaseCount(ctx, taskID, stageID)
	if err != nil {
		return err
	}
	if count < 3 {
		return nil
	}
	_ = k.db.FailQueuedStageMessages(ctx, taskID, ErrStageBudgetExceeded.Error(), stageID, agent)
	return ErrStageBudgetExceeded
}

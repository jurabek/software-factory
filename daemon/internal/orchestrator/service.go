// Package orchestrator is the factory control plane. It owns task identity and
// lifecycle controls, message and intervention intake, the worker registry,
// cancellation, and the hand-off to the fixed pipeline. It contains no stage,
// config, projection, anchor, recipient, or retry mechanics: those live in
// task, projection, intervention, messaging, and the workflow packages.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/intervention"
	"github.com/jurabek/software-factory/daemon/internal/messaging"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/planner"
	"github.com/jurabek/software-factory/daemon/internal/projection"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// ErrStalePlan reports an approval whose plan digest no longer matches.
var ErrStalePlan = errors.New("plan digest is stale")

// Dependencies are the collaborators the orchestrator composes.
type Dependencies struct {
	Store      *store.DB
	Config     config.Config
	ConfigPath string
	Harnesses  harness.Registry
	Git        factorygit.Runner
	Sandbox    workspace.Sandbox
	Workflow   *pipeline.Pipeline
}

// Service is the factory control plane.
type Service struct {
	root          string
	db            *store.DB
	mu            sync.Mutex
	cancel        map[string]*execution
	taskLocks     sync.Map
	tasks         *task.Service
	projections   *projection.Service
	interventions *intervention.Service
	messages      *messaging.Service
	workflow      *pipeline.Pipeline
}

// New constructs the orchestrator and its owned sub-services.
func New(root string, dependencies Dependencies) *Service {
	snapshots := workspace.New(dependencies.Store, dependencies.Git)
	interventions := intervention.New(intervention.Deps{
		Store: dependencies.Store, Git: dependencies.Git, Snapshots: snapshots,
		Config: dependencies.Config, ConfigPath: dependencies.ConfigPath, Root: root,
	})
	return &Service{
		root:   root,
		db:     dependencies.Store,
		cancel: map[string]*execution{},
		tasks: task.New(root, task.Deps{
			Store: dependencies.Store, Config: dependencies.Config, ConfigPath: dependencies.ConfigPath,
			Harnesses: dependencies.Harnesses, Git: dependencies.Git, Sandbox: dependencies.Sandbox,
		}),
		projections: projection.New(projection.Deps{
			Store: dependencies.Store, Config: dependencies.Config, ConfigPath: dependencies.ConfigPath,
		}),
		interventions: interventions,
		messages: messaging.New(messaging.Deps{
			Store: dependencies.Store, Config: dependencies.Config, ConfigPath: dependencies.ConfigPath,
			Harnesses: dependencies.Harnesses, Root: root, Interventions: interventions,
		}),
		workflow: dependencies.Workflow,
	}
}

// Create persists a task and starts its workflow.
func (s *Service) Create(ctx context.Context, request task.CreateRequest) (store.Task, error) {
	created, err := s.tasks.Create(ctx, request)
	if err != nil {
		return store.Task{}, fmt.Errorf("failed to create task: %w", err)
	}
	return s.launchCreatedTask(ctx, created)
}

// CreateSession starts a child task and its workflow.
func (s *Service) CreateSession(ctx context.Context, taskID string, request task.CreateSessionRequest) (store.Task, error) {
	created, err := s.tasks.CreateSession(ctx, taskID, request)
	if err != nil {
		return store.Task{}, fmt.Errorf("creating sessions failed: %w", err)
	}
	return s.launchCreatedTask(ctx, created)
}

func (s *Service) launchCreatedTask(ctx context.Context, task store.Task) (store.Task, error) {
	created, err := s.db.Task(ctx, task.ID)
	if err != nil {
		return store.Task{}, err
	}
	s.launch(task.ID, s.progress)
	return created, nil
}

// Approve records a human approval and resumes the workflow.
func (s *Service) Approve(ctx context.Context, id, actor, expectedDigest string) error {
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return fmt.Errorf("plan_digest is required")
	}
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != string(stagekit.AwaitingApproval) {
		return store.ErrConflict
	}
	phases, err := s.db.Phases(ctx, id)
	if err != nil {
		return err
	}
	planStageID := ""
	for index := len(phases) - 1; index >= 0; index-- {
		if phases[index].Kind == "plan" {
			planStageID = phases[index].Name
			break
		}
	}
	if planStageID == "" {
		return store.ErrNotFound
	}
	payload, err := s.db.ValidEnvelope(ctx, id, planStageID)
	if err != nil {
		return err
	}
	plan, err := planner.Validate(payload)
	if err != nil || len(plan.Questions) > 0 {
		return store.ErrConflict
	}
	reportDigest := ""
	artifacts, artifactErr := s.db.Artifacts(ctx, id)
	if artifactErr != nil {
		return artifactErr
	}
	for _, artifact := range artifacts {
		if artifact.AttemptID != "" && artifact.Type == "plan_report" {
			reportDigest = artifact.Digest
		}
	}
	currentDigest := stagekit.PlanApprovalDigest(payload, reportDigest)
	if expectedDigest != currentDigest {
		return ErrStalePlan
	}
	event := store.Event{
		ID: stagekit.RandomID(), TaskID: id, Kind: session.KindCustom, Name: "task_approved",
		Payload: session.CustomPayload{CustomType: "task_approved", Data: session.BoundedJSON(map[string]any{
			"task_id": id, "plan_digest": currentDigest, "actor": actor,
		})},
		Display:          session.Display{Role: "system", Status: "success", Title: "Plan approved"},
		AvailableActions: []string{"pause", "abort"}, StartedAt: time.Now().UTC(),
	}
	if err := s.db.ApproveWithEvent(ctx, s.taskDir(id), id, currentDigest, actor, event); err != nil {
		return err
	}
	s.launch(id, s.progress)
	return nil
}

// Pause stops a task's worker and marks it paused.
func (s *Service) Pause(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(stagekit.State(task.State), stagekit.Paused) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(stagekit.State(task.State), stagekit.Paused) {
		return store.ErrConflict
	}
	return s.db.Transition(ctx, id, task.State, string(stagekit.Paused), task.ActivePhase, "")
}

// Abort stops a task's worker and fails its queued messages.
func (s *Service) Abort(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(stagekit.State(task.State), stagekit.Aborted) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	var messages []store.Message
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(stagekit.State(task.State), stagekit.Aborted) {
		return store.ErrConflict
	}
	messages, err = s.db.AbortTask(ctx, id, task.State, task.ActivePhase)
	if err != nil {
		return err
	}
	for _, message := range messages {
		_ = s.traceMessage(ctx, message, nil)
	}
	return nil
}

// Resume relaunches a paused or blocked task; the eligible stage resumes from
// its durable result.
func (s *Service) Resume(ctx context.Context, id string) error {
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != string(stagekit.Paused) && task.State != string(stagekit.Blocked) {
		return store.ErrConflict
	}
	if task.State == string(stagekit.Blocked) && task.Error == "unresolved_questions" {
		return store.ErrConflict
	}
	s.launch(id, s.progress)
	return nil
}

// Delete removes a task and its child sessions.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.tasks.Delete(ctx, id)
}

// Diff returns the changed files and patch for a task.
func (s *Service) Diff(ctx context.Context, id string) (task.Diff, error) {
	return s.tasks.Diff(ctx, id)
}

// StageProjection projects a task's pipeline stages.
func (s *Service) StageProjection(ctx context.Context, value store.Task) ([]store.StageProjection, error) {
	return s.projections.StageProjection(ctx, value)
}

// SendMessage accepts a message and hands the task back to the workflow.
func (s *Service) SendMessage(ctx context.Context, taskID, actor string, request messaging.Request) (store.Message, error) {
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	stored, launch, err := s.messages.Send(ctx, taskID, actor, request)
	if err != nil {
		return store.Message{}, err
	}
	if launch {
		s.launch(taskID, s.progress)
	}
	return stored, nil
}

// Intervene applies an operator intent to a task.
func (s *Service) Intervene(ctx context.Context, taskID, actor string, request intervention.Request) (store.InterventionResult, error) {
	return s.interventions.Apply(ctx, taskID, actor, request)
}

// Retry queues a new attempt and hands the task back to the workflow.
func (s *Service) Retry(ctx context.Context, taskID, attemptID string, request intervention.RetryRequest) (store.RetryResult, error) {
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	result, created, err := s.interventions.Retry(ctx, taskID, attemptID, request)
	if err != nil {
		return store.RetryResult{}, err
	}
	if created {
		s.launch(taskID, s.progress)
	}
	return result, nil
}

func (s *Service) taskDir(id string) string { return filepath.Join(s.root, "tasks", id) }

func (s *Service) taskLock(id string) *sync.Mutex {
	value, _ := s.taskLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// Package orchestrator is the factory control plane. It owns the worker
// registry, cancellation, shutdown, and the durable-event loop that hands tasks
// to the fixed pipeline. It contains no stage, config, projection, recipient,
// retry, or task-allocation mechanics: those live in their own modules and
// publish events for the orchestrator to consume.
package orchestrator

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Dependencies are the collaborators the orchestrator consumes.
type Dependencies struct {
	Store    *store.DB
	Workflow *pipeline.Pipeline
	Events   *Events
}

// Service is the factory control plane.
type Service struct {
	root      string
	db        *store.DB
	mu        sync.Mutex
	cancel    map[string]*execution
	taskLocks sync.Map
	workflow  *pipeline.Pipeline
	events    *Events
}

// New constructs the orchestrator.
func New(root string, dependencies Dependencies) *Service {
	events := dependencies.Events
	if events == nil {
		events = NewEvents(dependencies.Store)
	}
	return &Service{
		root:     root,
		db:       dependencies.Store,
		cancel:   map[string]*execution{},
		workflow: dependencies.Workflow,
		events:   events,
	}
}

func (s *Service) pause(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State == string(stagekit.Paused) {
		return nil
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

func (s *Service) abort(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State == string(stagekit.Aborted) {
		return nil
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

func (s *Service) taskDir(id string) string { return filepath.Join(s.root, "tasks", id) }

func (s *Service) taskLock(id string) *sync.Mutex {
	value, _ := s.taskLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

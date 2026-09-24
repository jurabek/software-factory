// Package orchestrator is the factory control plane. It owns the worker
// registry, cancellation, shutdown, and the durable-event loop that hands tasks
// to the fixed pipeline. It contains no stage, config, projection, recipient,
// retry, or task-allocation mechanics: those live in their own modules and
// publish events for the orchestrator to consume.
package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

const (
	Preparing        = stagekit.Preparing
	Planning         = stagekit.Planning
	AwaitingApproval = stagekit.AwaitingApproval
	Building         = stagekit.Building
	Checking         = stagekit.Checking
	Reviewing        = stagekit.Reviewing
	Completed        = stagekit.Completed
	Blocked          = stagekit.Blocked
	Paused           = stagekit.Paused
	Aborted          = stagekit.Aborted
)

type execution struct {
	cancel context.CancelFunc
	done   chan struct{}
	next   func(context.Context, string) error
}

// Dependencies are the collaborators the orchestrator consumes.
type Dependencies struct {
	Store    *store.Store
	Workflow *pipeline.Pipeline
	Events   *Events
}

// Service is the factory control plane.
type Service struct {
	root      string
	db        *store.Store
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
	task, err := s.db.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if task.State == stagekit.Paused {
		return nil
	}
	if !stagekit.CanTransition(task.State, stagekit.Paused) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err = s.db.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(task.State, stagekit.Paused) {
		return store.ErrConflict
	}
	return s.db.Tasks.Transition(ctx, id, task.State, stagekit.Paused, task.ActivePhase, "")
}

func (s *Service) abort(ctx context.Context, id string) error {
	task, err := s.db.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if task.State == stagekit.Aborted {
		return nil
	}
	if !stagekit.CanTransition(task.State, stagekit.Aborted) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	var messages []store.Message
	task, err = s.db.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if !stagekit.CanTransition(task.State, stagekit.Aborted) {
		return store.ErrConflict
	}
	messages, err = s.db.Tasks.Abort(ctx, id, task.State, task.ActivePhase)
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

func (s *Service) HandleEvents(ctx context.Context) {
	pending, err := s.db.Orchestration.Pending(ctx)
	if err == nil {
		for _, event := range pending {
			if ctx.Err() != nil {
				return
			}
			s.handleQueuedEvent(ctx, event.ID)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.events.ids:
			if ctx.Err() != nil {
				return
			}
			s.handleQueuedEvent(ctx, id)
		}
	}
}

func (s *Service) handleQueuedEvent(ctx context.Context, id string) {
	err := s.handleEvent(ctx,
		id)
	if ctx.Err() == nil {
		s.events.complete(id, err)
	}
}

func (s *Service) handleEvent(ctx context.Context, id string) error {
	event, err := s.db.Orchestration.Get(ctx, id)
	if err != nil {
		return err
	}
	switch event.Type {
	case store.TaskCreated, store.TaskMessaged, store.TaskRetried:
		task, taskErr := s.db.Tasks.Get(ctx, event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State != stagekit.Paused && task.State != stagekit.Aborted {
			s.launch(task.ID, s.progress)
		}
	case store.TaskResumed:
		task, taskErr := s.db.Tasks.Get(ctx, event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State == stagekit.Paused {
			if task.PreviousState == "" {
				err = store.ErrConflict
			} else {
				err = s.db.Tasks.Transition(ctx, task.ID, task.State, task.PreviousState, task.ActivePhase, "")
				if err == nil {
					s.launch(task.ID, s.progress)
				}
			}
		} else if task.State == stagekit.Blocked {
			s.launch(task.ID, s.progress)
		} else {
			err = store.ErrConflict
		}
	case store.TaskApproved:
		task, taskErr := s.db.Tasks.Get(ctx,
			event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State == stagekit.AwaitingApproval {
			err = s.db.Tasks.Transition(ctx, task.ID, task.State, stagekit.Building, task.ActivePhase, "")
			if err == nil {
				s.launch(task.ID,
					s.progress)
			}
		} else if task.State != stagekit.Building {
			err = store.ErrConflict
		}
	case store.TaskPaused:
		err = s.pause(ctx, event.TaskID)
	case store.TaskCancelled:
		err = s.abort(ctx, event.TaskID)
	default:
		err = errors.New("unknown orchestration event")
	}
	return err
}

func (s *Service) progress(ctx context.Context, taskID string) error {
	if s.workflow == nil {
		return nil
	}
	_, err := s.workflow.Run(ctx, taskID)
	return err
}

func (s *Service) launch(id string, run func(context.Context, string) error) {
	s.mu.Lock()
	if active := s.cancel[id]; active != nil {
		if active.next == nil {
			active.next = run
		}
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &execution{cancel: cancel, done: make(chan struct{})}
	s.cancel[id] = active
	s.mu.Unlock()

	s.runExecution(ctx, id, active, run)
}

func (s *Service) runExecution(ctx context.Context, id string, active *execution, run func(context.Context, string) error) {
	go func() {
		var runErr error
		defer func() {
			var next func(context.Context, string) error
			var successor *execution
			var successorCtx context.Context
			s.mu.Lock()
			if s.cancel[id] == active {
				next = active.next
				if next == nil {
					delete(s.cancel, id)
				} else {
					var cancel context.CancelFunc
					successorCtx, cancel = context.WithCancel(context.Background())
					successor = &execution{cancel: cancel, done: make(chan struct{})}
					s.cancel[id] = successor
				}
			}
			s.mu.Unlock()
			close(active.done)
			if successor != nil {
				s.runExecution(successorCtx,
					id, successor, next)
			} else if !errors.Is(runErr, context.Canceled) {
				s.kickQueuedMessage(id)
			}
		}()
		runErr = run(ctx, id)
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			task, getErr := s.db.Tasks.Get(context.Background(),
				id)
			if getErr == nil && task.State != stagekit.Paused && task.State != stagekit.Aborted && task.State != stagekit.Blocked {
				_ = s.db.Tasks.Transition(context.Background(), id, task.State, stagekit.Blocked, task.ActivePhase,
					runErr.Error())
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	workers := make(map[string]*execution, len(s.cancel))
	for id, worker := range s.cancel {
		workers[id] = worker
		worker.next = nil
		worker.cancel()
	}
	s.mu.Unlock()
	for id, worker := range workers {
		select {
		case <-worker.done:
		case <-ctx.Done():
			return
		}
		task, err := s.db.Tasks.Get(ctx, id)
		if err == nil && stagekit.IsActive(task.State) {
			_ = s.db.Tasks.Transition(ctx, id, task.State, stagekit.Blocked, task.ActivePhase,
				"server shutting down")
		}
	}
}

func (s *Service) stopAndWait(ctx context.Context, id string) error {
	for {
		s.mu.Lock()
		active := s.cancel[id]
		if active != nil {
			active.next = nil

			active.cancel()
		}
		s.mu.Unlock()
		if active == nil {
			return nil
		}
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) scheduleMessage(ctx context.Context, task store.Task, role string) error {
	_ = role
	switch task.State {
	case stagekit.AwaitingApproval, stagekit.Blocked, stagekit.Completed:
		return s.events.Publish(ctx,
			task.ID, store.TaskMessaged)
	}
	return nil
}

func (s *Service) kickQueuedMessage(taskID string) {
	ctx := context.Background()
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := s.db.Tasks.Get(ctx, taskID)
	if err != nil {
		return
	}
	if task.State != stagekit.AwaitingApproval && task.State != stagekit.Blocked && task.State != stagekit.Completed {
		return
	}
	message, err := s.db.Messages.NextQueuedForTask(ctx, taskID)
	if err != nil {
		return
	}
	_ = s.scheduleMessage(ctx, task, message.StageID)
}

func (s *Service) traceMessage(ctx context.Context, message store.Message, phase *store.Phase) error {
	event, err := stagekit.MessageEvent(ctx, s.db, message, phase)
	if err != nil {
		return err
	}
	_, err = s.db.Events.Append(ctx, s.taskDir(message.TaskID), event)
	return err
}

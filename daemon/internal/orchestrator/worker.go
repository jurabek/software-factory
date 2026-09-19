package orchestrator

import (
	"context"
	"errors"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type execution struct {
	cancel context.CancelFunc
	done   chan struct{}
	next   func(context.Context, string) error
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
				s.runExecution(successorCtx, id, successor, next)
			} else if !errors.Is(runErr, context.Canceled) {
				s.kickQueuedMessage(id)
			}
		}()
		runErr = run(ctx, id)
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			task, getErr := s.db.Task(context.Background(), id)
			if getErr == nil && task.State != string(stagekit.Paused) && task.State != string(stagekit.Aborted) && task.State != string(stagekit.Blocked) {
				_ = s.db.Transition(context.Background(), id, task.State, string(stagekit.Blocked), task.ActivePhase, runErr.Error())
			}
		}
	}()
}

// Shutdown cancels every worker and blocks active tasks.
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
		task, err := s.db.Task(ctx, id)
		if err == nil && stagekit.IsActive(stagekit.State(task.State)) {
			_ = s.db.Transition(ctx, id, task.State, string(stagekit.Blocked), task.ActivePhase, "server shutting down")
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
	switch stagekit.State(task.State) {
	case stagekit.AwaitingApproval, stagekit.Blocked, stagekit.Completed:
		return s.events.Publish(ctx, task.ID, store.TaskMessaged)
	}
	return nil
}

func (s *Service) kickQueuedMessage(taskID string) {
	ctx := context.Background()
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return
	}
	if task.State != string(stagekit.AwaitingApproval) && task.State != string(stagekit.Blocked) && task.State != string(stagekit.Completed) {
		return
	}
	message, err := s.db.NextQueuedTaskMessage(ctx, taskID)
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
	_, err = s.db.AppendEvent(ctx, s.taskDir(message.TaskID), event)
	return err
}

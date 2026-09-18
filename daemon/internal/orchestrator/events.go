package orchestrator

import (
	"context"
	"errors"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func (s *Service) replayEvents() {
	_ = s.events.replay(context.Background())
}

func (s *Service) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.events.ids:
			err := s.handleEvent(ctx, id)
			s.events.complete(id, err)
		}
	}
}

func (s *Service) handleEvent(ctx context.Context, id string) error {
	event, err := s.db.OrchestrationEvent(ctx, id)
	if err != nil {
		return err
	}
	switch event.Type {
	case store.TaskCreated, store.TaskMessaged, store.TaskRetried:
		task, taskErr := s.db.Task(ctx, event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State != string(stagekit.Paused) && task.State != string(stagekit.Aborted) {
			s.launch(task.ID, s.progress)
		}
	case store.TaskResumed:
		task, taskErr := s.db.Task(ctx, event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State == string(stagekit.Paused) {
			if task.PreviousState == "" {
				err = store.ErrConflict
			} else {
				err = s.db.Transition(ctx, task.ID, task.State, task.PreviousState, task.ActivePhase, "")
				if err == nil {
					s.launch(task.ID, s.progress)
				}
			}
		} else if task.State == string(stagekit.Blocked) {
			s.launch(task.ID, s.progress)
		} else {
			err = store.ErrConflict
		}
	case store.TaskApproved:
		task, taskErr := s.db.Task(ctx, event.TaskID)
		if taskErr != nil {
			err = taskErr
		} else if task.State == string(stagekit.AwaitingApproval) {
			err = s.db.Transition(ctx, task.ID, task.State, string(stagekit.Building), task.ActivePhase, "")
			if err == nil {
				s.launch(task.ID, s.progress)
			}
		} else if task.State != string(stagekit.Building) {
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

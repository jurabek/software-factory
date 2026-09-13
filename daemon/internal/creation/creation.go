// Package creation owns the internal creation stage: task allocation followed
// by repository preparation before the configured pipeline begins.
package creation

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
)

// Service is the creation stage's public surface.
type Service struct {
	tasks  *task.Service
	kit    *stagekit.Kit
	events *orchestrator.Events
}

func New(tasks *task.Service, kit *stagekit.Kit, events *orchestrator.Events) *Service {
	return &Service{tasks: tasks, kit: kit, events: events}
}

// Create allocates a task. Repository preparation is intentionally deferred to
// Prepare so callers can return an identity before background work begins.
func (s *Service) Create(ctx context.Context, request task.CreateRequest) (store.Task, error) {
	created, err := s.tasks.Create(ctx, request)
	if err != nil || s.events == nil {
		return created, err
	}
	return created, s.events.Publish(ctx, created.ID, store.TaskCreated)
}

func (s *Service) CreateSession(ctx context.Context, taskID string, request task.CreateSessionRequest) (store.Task, error) {
	created, err := s.tasks.CreateSession(ctx, taskID, request)
	if err != nil || s.events == nil {
		return created, err
	}
	return created, s.events.Publish(ctx, created.ID, store.TaskCreated)
}

// Prepare completes the internal creation stage. It is safe to repeat because
// stagekit reuses an already materialized repository.
func (s *Service) Prepare(ctx context.Context, input stage.Input) error {
	task, err := s.kit.Task(ctx, input.TaskID)
	if err != nil {
		return err
	}
	_, err = s.kit.EnsurePrepared(ctx, task)
	return err
}

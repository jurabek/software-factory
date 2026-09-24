package store

import (
	"context"
	"database/sql"
	"fmt"
)

// OrchestrationEvent is a durable command for the background task controller.
// Delivery is at-least-once; handlers must derive their effects from task state.
type OrchestrationEvent struct {
	ID     string
	TaskID string
	Type   string
}

const (
	TaskCreated   = "task_created"
	TaskPaused    = "task_paused"
	TaskCancelled = "task_cancelled"
	TaskMessaged  = "task_messaged"
	TaskApproved  = "task_approved"
	TaskResumed   = "task_resumed"
	TaskRetried   = "task_retried"
)

// EnqueueOrchestrationEvent records a command after its durable task mutation.

type OrchestrationRepository struct{ db *sql.DB }

func (r *OrchestrationRepository) Enqueue(ctx context.Context, event OrchestrationEvent,
) error {
	_, err := r.db.ExecContext(ctx, `insert into orchestration_events(id,task_id,type,created_at) values(?,?,?,?)`, event.ID, event.TaskID, event.Type, now())
	return wrap(
		"enqueue orchestration event", err)
}

func (r *OrchestrationRepository) Get(ctx context.Context, id string,
) (OrchestrationEvent, error) {
	var event OrchestrationEvent
	err := r.db.QueryRowContext(ctx, `select id,task_id,type from orchestration_events where id=?`,
		id,
	).Scan(&event.ID, &event.TaskID, &event.Type)
	if err == sql.ErrNoRows {
		return OrchestrationEvent{}, ErrNotFound
	}
	return event, wrap("read orchestration event", err)
}

func (r *OrchestrationRepository) Pending(
	ctx context.Context,
) ([]OrchestrationEvent, error) {
	rows, err := r.db.QueryContext(ctx, `select id,task_id,type from orchestration_events where status='pending' order by created_at,id`)
	if err != nil {
		return nil, wrap("list pending orchestration events",
			err)
	}
	defer rows.Close()
	values := []OrchestrationEvent{}
	for rows.Next() {
		var event OrchestrationEvent
		if err = rows.Scan(&event.ID, &event.TaskID, &event.Type); err != nil {
			return nil, fmt.Errorf("scan orchestration event: %w", err)
		}
		values = append(values, event)
	}
	return values, rows.Err()
}

func (r *OrchestrationRepository) Complete(
	ctx context.Context, id string, cause error,
) error {
	status, message := "handled", ""
	if cause != nil {
		status, message = "pending",
			cause.Error()
	}
	_, err := r.db.ExecContext(ctx,
		`update orchestration_events set status=?,error=?,handled_at=? where id=?`,
		status, nullIfEmpty(message), now(), id)
	return wrap("complete orchestration event", err)
}

// StartPhaseWithEvent creates a running phase, updates its Task's active phase

// and branch head, and records the phase-start event atomically.

package orchestrator

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Events persists commands before notifying the background event handler. The
// channel is only a wakeup; pending rows are replayed after a daemon restart.
type Events struct {
	db  *store.DB
	ids chan string
}

func NewEvents(db *store.DB) *Events {
	return &Events{db: db, ids: make(chan string, 128)}
}

func (e *Events) Publish(ctx context.Context, taskID, kind string) error {
	id := stagekit.RandomID()
	if err := e.db.EnqueueOrchestrationEvent(ctx, store.OrchestrationEvent{ID: id, TaskID: taskID, Type: kind}); err != nil {
		return err
	}
	e.ids <- id
	return nil
}

func (e *Events) replay(ctx context.Context) error {
	pending, err := e.db.PendingOrchestrationEvents(ctx)
	if err != nil {
		return err
	}
	for _, event := range pending {
		e.ids <- event.ID
	}
	return nil
}

func (e *Events) complete(id string, err error) {
	_ = e.db.CompleteOrchestrationEvent(context.Background(), id, err)
}

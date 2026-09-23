package orchestrator

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Events persists commands before notifying the background event handler. The
// channel is only a wakeup; pending rows are replayed after a daemon restart.
type Events struct {
	db  *store.Store
	ids chan string
}

func NewEvents(db *store.Store) *Events {
	return &Events{db: db, ids: make(chan string, 128)}
}

func (e *Events) Publish(ctx context.Context, taskID, kind string) error {
	id := stagekit.RandomID()
	if err := e.db.Orchestration.Enqueue(ctx, store.OrchestrationEvent{ID: id, TaskID: taskID, Type: kind}); err != nil {
		return err
	}
	e.ids <- id
	return nil
}

func (e *Events) complete(id string, err error) {
	_ = e.db.Orchestration.Complete(context.Background(), id, err)
}

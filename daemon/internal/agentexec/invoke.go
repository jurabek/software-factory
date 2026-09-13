package agentexec

import (
	"context"
	"errors"
	"sync"

	"github.com/jurabek/software-factory/daemon/internal/harness"
)

func Invoke(ctx context.Context, adapter harness.Harness, request harness.Request, sink harness.EventSink) (harness.Result, error) {
	invocationCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var sinkErr error
	requiredSink := func(eventCtx context.Context, event harness.Event) error {
		err := sink(eventCtx, event)
		if err == nil {
			return nil
		}
		mu.Lock()
		if sinkErr == nil {
			sinkErr = err
		}
		mu.Unlock()
		cancel()
		return err
	}

	result, runErr := adapter.Run(invocationCtx, request, requiredSink)
	mu.Lock()
	persistenceErr := sinkErr
	mu.Unlock()
	if persistenceErr != nil {
		return result, errors.Join(persistenceErr, runErr)
	}
	if runErr == nil {
		runErr = invocationCtx.Err()
	}
	return result, runErr
}

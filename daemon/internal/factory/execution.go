package factory

import (
	"context"
	"errors"
	"sync"
)

var ErrStaleExecution = errors.New("execution is no longer current")

type executionContextKey struct{}

type executionOwner struct {
	mu      sync.Mutex
	active  map[string]*execution
	locks   map[string]*sync.Mutex
	stopped bool
}

type execution struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

func newExecutionOwner() *executionOwner {
	return &executionOwner{
		active: make(map[string]*execution),
		locks:  make(map[string]*sync.Mutex),
	}
}

func (o *executionOwner) taskLock(taskID string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	lock := o.locks[taskID]
	if lock == nil {
		lock = &sync.Mutex{}
		o.locks[taskID] = lock
	}
	return lock
}

func (o *executionOwner) withTask(taskID string, run func() error) error {
	lock := o.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	return run()
}

func (o *executionOwner) start(taskID, executionID string, run func(context.Context) error, finished func(context.Context, error)) bool {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, executionContextKey{}, executionID)
	current := &execution{id: executionID, cancel: cancel, done: make(chan struct{})}

	o.mu.Lock()
	if o.stopped || o.active[taskID] != nil {
		o.mu.Unlock()
		cancel()
		return false
	}
	o.active[taskID] = current
	o.mu.Unlock()

	go func() {
		runErr := run(ctx)
		o.mu.Lock()
		if o.active[taskID] == current {
			delete(o.active, taskID)
		}
		o.mu.Unlock()
		if finished != nil {
			finished(ctx, runErr)
		}
		close(current.done)
	}()
	return true
}

func (o *executionOwner) stopAndWait(ctx context.Context, taskID string) error {
	o.mu.Lock()
	current := o.active[taskID]
	o.mu.Unlock()
	if current == nil {
		return nil
	}
	current.cancel()
	select {
	case <-current.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *executionOwner) stopAll() []string {
	o.mu.Lock()
	o.stopped = true
	ids := make([]string, 0, len(o.active))
	for taskID, current := range o.active {
		ids = append(ids, taskID)
		current.cancel()
	}
	o.mu.Unlock()
	return ids
}

func (o *executionOwner) wait(ctx context.Context) error {
	o.mu.Lock()
	workers := make([]*execution, 0, len(o.active))
	for _, current := range o.active {
		workers = append(workers, current)
	}
	o.mu.Unlock()
	var waitErr error
	for _, current := range workers {
		select {
		case <-current.done:
		case <-ctx.Done():
			waitErr = ctx.Err()
			<-current.done
		}
	}
	return waitErr
}

func (o *executionOwner) resume() {
	o.mu.Lock()
	o.stopped = false
	o.mu.Unlock()
}

func (o *executionOwner) current(taskID, executionID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	current := o.active[taskID]
	return current != nil && current.id == executionID
}

func executionID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(executionContextKey{}).(string)
	return id, ok && id != ""
}

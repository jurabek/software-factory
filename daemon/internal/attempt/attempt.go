// Package attempt owns the Attempt: one immutable execution of a Task phase.
// It resolves the current attempt of a stage, decides whether a durable
// attempt is still eligible for reuse downstream, and serializes per-task
// mutations with one lock shared by stages and the control plane.
package attempt

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Phases is the phase history an attempt is resolved from.
type Phases interface {
	List(ctx context.Context, taskID string) ([]store.Phase, error)
	ByID(ctx context.Context, taskID, phaseID string) (store.Phase, error)
}

// Ledger resolves attempts against phase history.
type Ledger struct {
	phases Phases
}

// NewLedger constructs an attempt ledger.
func NewLedger(phases Phases) Ledger { return Ledger{phases: phases} }

// Latest returns the newest non-superseded attempt of a stage.
func (l Ledger) Latest(ctx context.Context, taskID, stageID string) (store.Phase, bool, error) {
	phases, err := l.phases.List(ctx, taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {
		if phase.Name == stageID && !phase.Superseded {
			return phase, true, nil
		}
	}
	return store.Phase{}, false, nil
}

// Successful returns the stage's current attempt when it succeeded.
func (l Ledger) Successful(ctx context.Context, taskID, stageID string) (store.Phase, bool, error) {
	phase, ok, err := l.Latest(ctx, taskID, stageID)
	if err != nil || !ok || phase.Status != "success" {
		return store.Phase{}, false, err
	}
	return phase, true, nil
}

// Require fails unless attemptID is still the stage's current successful attempt.
func (l Ledger) Require(ctx context.Context, taskID, stageID, attemptID string) error {
	phase, ok, err := l.Successful(ctx, taskID, stageID)
	if err != nil {
		return err
	}
	if !ok || phase.ID != attemptID {
		return fmt.Errorf("%s result %s is no longer eligible", stageID, attemptID)
	}
	return nil
}

// After reports whether attemptID was produced after upstreamID.
func (l Ledger) After(ctx context.Context, taskID, attemptID, upstreamID string) (bool, error) {
	attempt, err := l.phases.ByID(ctx, taskID, attemptID)
	if err != nil {
		return false, err
	}
	upstream, err := l.phases.ByID(ctx, taskID, upstreamID)
	if err != nil {
		return false, err
	}
	return EligibleAfter(attempt.Sequence, upstream.Sequence), nil
}

// Resume returns the stage's current successful attempt when it postdates
// upstreamID (empty means no upstream), so it may be reused without rerunning.
func (l Ledger) Resume(ctx context.Context, taskID, stageID, upstreamID string) (store.Phase, bool, error) {
	phase, ok, err := l.Successful(ctx, taskID, stageID)
	if err != nil || !ok {
		return store.Phase{}, false, err
	}
	if upstreamID != "" {
		eligible, err := l.After(ctx, taskID, phase.ID, upstreamID)
		if err != nil || !eligible {
			return store.Phase{}, false, err
		}
	}
	return phase, true, nil
}

// EligibleAfter owns downstream lineage: a result is reusable only when it was
// produced after its upstream result.
func EligibleAfter(attemptSequence, upstreamSequence int) bool {
	return attemptSequence > upstreamSequence
}

// Locks hands out one mutex per task. Stages lock only around begin and
// drain-then-publish, never during agent execution; the control plane locks
// around pause, abort, and message kicks.
type Locks struct{ values sync.Map }

// For returns the mutex for a task.
func (l *Locks) For(taskID string) *sync.Mutex {
	value, _ := l.values.LoadOrStore(taskID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

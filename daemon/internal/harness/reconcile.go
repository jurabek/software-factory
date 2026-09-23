package harness

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// ReconcileDB is the persistence surface startup reconciliation needs.
type ReconcileDB interface {
	PendingAgentSessions(ctx context.Context) ([]store.AgentSession, error)
	ReconcileAgentStats(ctx context.Context, taskID, stageID string, value store.AgentSession) error
	RequeueInterruptedPhase(ctx context.Context, taskID, phaseID string) error
}

// ReconcilePendingTurns reconciles Turns that were in flight when the daemon
// stopped. It derives usage, cost, and the session leaf from the native
// session, then returns the in-flight phase to the queue so an explicit resume
// reuses it. The turn itself is resolved later, inside RunTurn, where the
// stage's envelope validator is available.
func ReconcilePendingTurns(ctx context.Context, db ReconcileDB, reader NativeReader) error {
	if reader == nil {
		return nil
	}
	sessions, err := db.PendingAgentSessions(ctx)
	if err != nil {
		return err
	}
	for _, pending := range sessions {
		if pending.HarnessSessionID == "" || pending.SessionDirectory == "" {
			continue
		}
		ref := SessionRef{ID: pending.HarnessSessionID, Directory: pending.SessionDirectory}
		if stats, statsErr := reader.Stats(ctx, ref); statsErr == nil {
			updated := pending
			updated.Usage = persistedUsage(stats.Usage)
			updated.Cost = stats.Usage.Cost
			if stats.LeafID != "" {
				updated.LastEntryID = stats.LeafID
			}
			if stats.ContextTokens > 0 {
				updated.ContextTokens = stats.ContextTokens
			}
			if stats.ContextWindow > 0 {
				updated.ContextWindow = stats.ContextWindow
			}
			if err = db.ReconcileAgentStats(ctx, pending.TaskID, pending.StageID, updated); err != nil {
				return err
			}
		}
		if pending.PendingPhaseID != "" {
			if err = db.RequeueInterruptedPhase(ctx, pending.TaskID, pending.PendingPhaseID); err != nil {
				return err
			}
		}
	}
	return nil
}

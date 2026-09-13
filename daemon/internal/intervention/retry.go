package intervention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Retry queues a new attempt for a failed phase, reusing its original input
// snapshot. The returned bool reports whether a new retry was created.
func (s *Service) Retry(ctx context.Context, taskID, attemptID string, request RetryRequest) (store.RetryResult, bool, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return store.RetryResult{}, false, fmt.Errorf("idempotency_key is required")
	}
	if existing, err := s.deps.Store.RetryByIdempotencyKey(ctx, taskID, request.IdempotencyKey); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.RetryResult{}, false, err
	}
	task, err := s.deps.Store.Task(ctx, taskID)
	if err != nil {
		return store.RetryResult{}, false, err
	}
	switch stagekit.State(task.State) {
	case stagekit.Preparing, stagekit.Planning, stagekit.Building, stagekit.Checking, stagekit.Reviewing:
		return store.RetryResult{}, false, store.ErrConflict
	}
	phase, err := s.deps.Store.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return store.RetryResult{}, false, err
	}
	if phase.Status == "running" || phase.Status == "queued" {
		return store.RetryResult{}, false, store.ErrConflict
	}
	if phase.InputSnapshot == "" {
		return store.RetryResult{}, false, fmt.Errorf("attempt input snapshot is required")
	}
	if task.RepositoryPath != "" {
		branchName := "software-factory/retry/" + stagekit.RandomID()
		if err = factorygit.RestoreForRetry(ctx, s.deps.Git, task.RepositoryType, task.CanonicalRepositoryPath, task.RepositoryPath, task.BaseSHA, branchName); err != nil {
			return store.RetryResult{}, false, err
		}
		if _, err = s.deps.Store.ExecContext(ctx, `update tasks set review_base_sha=?,branch_name=? where id=?`, task.ReviewBaseSHA, branchName, taskID); err != nil {
			return store.RetryResult{}, false, err
		}
	}
	if err = s.deps.Snapshots.MaterializeSnapshot(ctx, task, phase.InputSnapshot); err != nil {
		return store.RetryResult{}, false, err
	}
	parentBranch := task.SelectedBranchID
	if phase.BranchID != "" {
		parentBranch = phase.BranchID
	}
	branch := store.Branch{ID: stagekit.RandomID(), TaskID: taskID, ParentBranchID: parentBranch, ForkAttemptID: phase.ID, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	phases, err := s.deps.Store.Phases(ctx, taskID)
	if err != nil {
		return store.RetryResult{}, false, err
	}
	retry := store.Phase{
		ID: stagekit.RandomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: phase.Name, Kind: phase.Kind, Owner: phase.Owner,
		Description: phase.Description, Status: "queued", Attempt: phase.Attempt + 1, BranchID: branch.ID,
		DefinitionID: phase.DefinitionID, InputSnapshot: phase.InputSnapshot,
	}
	result, created, err := s.deps.Store.ApplyRetry(ctx, request.IdempotencyKey, branch, retry, string(stagekit.StateForPhase(phase)))
	if err != nil {
		return store.RetryResult{}, false, err
	}
	return result, created, nil
}

package task

import (
	"context"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func (s *Service) ensureBranch(ctx context.Context, taskID, parent string) error {
	task, err := s.deps.Store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.SelectedBranchID != "" {
		return nil
	}
	branches, err := s.deps.Store.Branches(ctx, taskID)
	if err != nil {
		return err
	}
	if len(branches) > 0 {
		return s.deps.Store.SelectBranch(ctx, taskID, branches[0].ID)
	}
	branch := store.Branch{ID: stagekit.RandomID(), TaskID: taskID, ParentBranchID: parent, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = s.deps.Store.CreateBranch(ctx, branch); err != nil {
		return err
	}
	return s.deps.Store.SelectBranch(ctx, taskID, branch.ID)
}

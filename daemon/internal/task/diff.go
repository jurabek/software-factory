package task

import (
	"context"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Diff returns the changed files and patch for a task branch.
func (s *Service) Diff(ctx context.Context, id string) (Diff, error) {
	task, err := s.deps.Store.Task(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	return s.diffRepository(ctx, task, false)
}

func (s *Service) diffRepository(ctx context.Context, task store.Task, reviewBase bool) (Diff, error) {
	base := task.BaseSHA
	if reviewBase && task.ReviewBaseSHA != "" {
		base = task.ReviewBaseSHA
	}
	files, err := factorygit.ChangedFiles(ctx, s.deps.Git, task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	patch, err := factorygit.Diff(ctx, s.deps.Git, task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	return Diff{Files: files, Patch: patch}, nil
}

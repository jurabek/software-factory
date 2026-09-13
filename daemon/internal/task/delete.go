package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// Delete removes a task and its child sessions once none are active.
func (s *Service) Delete(ctx context.Context, id string) error {
	task, err := s.deps.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	tasks := []store.Task{task}
	if task.ParentTaskID == "" {
		tasks, err = s.deps.Store.TaskSessions(ctx, id)
		if err != nil {
			return err
		}
	}
	for _, session := range tasks {
		if stagekit.IsActive(stagekit.State(session.State)) {
			return store.ErrConflict
		}
	}
	for _, session := range tasks {
		if s.deps.Sandbox != nil {
			_ = s.deps.Sandbox.Cleanup(ctx, workspace.CleanupRequest{TaskID: session.ID, WorkspaceRoot: session.WorkspacePath, SourceType: session.RepositoryType, CanonicalPath: session.CanonicalRepositoryPath, WorkingPath: session.RepositoryPath})
		}
		if err := os.RemoveAll(filepath.Join(s.root, "tasks", session.ID)); err != nil {
			return fmt.Errorf("remove task files: %w", err)
		}
	}
	return s.deps.Store.DeleteTask(ctx, id)
}

package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Fingerprint returns an empty fingerprint when the task has no repository,
// matching orchestrator expectations for unprepared tasks.
func Fingerprint(ctx context.Context, runner factorygit.Runner, task store.Task) (string, error) {
	if task.RepositoryPath == "" {
		return "", nil
	}
	return factorygit.Fingerprint(ctx, runner, task.RepositoryPath)
}

// ChangedFiles lists files changed since base (or review base when review is true).
func ChangedFiles(ctx context.Context, runner factorygit.Runner, task store.Task, reviewBase bool) ([]string, error) {
	base := task.BaseSHA
	if reviewBase && task.ReviewBaseSHA != "" {
		base = task.ReviewBaseSHA
	}
	return factorygit.ChangedFiles(ctx, runner, task.RepositoryPath, base)
}

// ReviewBase resolves the comparison base for evidence and review prompts.
func ReviewBase(task store.Task) string {
	if task.ReviewBaseSHA != "" {
		return task.ReviewBaseSHA
	}
	return task.BaseSHA
}

// ReadProfile decodes the materialized repository profile for stage evidence.
func ReadProfile(task store.Task) (Materialization, error) {
	body, err := os.ReadFile(filepath.Join(task.WorkspacePath, "repository-profile.json"))
	if err != nil {
		return Materialization{}, fmt.Errorf("read repository profile: %w", err)
	}
	var profile Materialization
	if err = json.Unmarshal(body, &profile); err != nil {
		return Materialization{}, fmt.Errorf("decode repository profile: %w", err)
	}
	return profile, nil
}

package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
)

// Git materializes a task repository using Git worktrees and GitHub clones.
type Git struct{}

var _ Sandbox = Git{}

func (Git) Materialize(ctx context.Context, request MaterializationRequest) (Materialization, error) {
	if request.TaskID == "" || request.Destination == "" {
		return Materialization{}, fmt.Errorf("task identity and destination are required")
	}
	var profile factorygit.Profile
	var err error
	switch request.SourceType {
	case "local":
		profile, err = factorygit.PrepareLocal(request.Source, request.Destination)
		if err != nil {
			return Materialization{}, fmt.Errorf("materialize local repository: %w", err)
		}
	case "github":
		profile, err = factorygit.PrepareGitHub(ctx, request.Source, request.Destination)
		if err != nil {
			return Materialization{}, fmt.Errorf("materialize github repository: %w", err)
		}
	default:
		return Materialization{}, fmt.Errorf("unsupported repository source type %q", request.SourceType)
	}
	return profile, nil
}

func (Git) Cleanup(ctx context.Context, request CleanupRequest) error {
	if request.TaskID == "" {
		return fmt.Errorf("task identity is required")
	}
	if request.SourceType != "local" || request.CanonicalPath == "" || request.WorkingPath == "" {
		return nil
	}
	if !ownedWorkingPath(request.WorkspaceRoot, request.WorkingPath) {
		return fmt.Errorf("repository sandbox is not owned by task")
	}
	if err := factorygit.RemoveWorktree(request.CanonicalPath, request.WorkingPath); err != nil {
		return fmt.Errorf("remove repository sandbox: %w", err)
	}
	return nil
}

func ownedWorkingPath(workspaceRoot, workingPath string) bool {
	if workspaceRoot == "" {
		return false
	}
	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return false
	}
	path, err := filepath.Abs(filepath.Clean(workingPath))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == filepath.Join("workspace", "repository") && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

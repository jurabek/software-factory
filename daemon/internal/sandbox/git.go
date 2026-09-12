package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/factory"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
)

// Git materializes a task repository using Git worktrees and GitHub clones.
type Git struct {
	Runner factorygit.Runner
}

var _ factory.Sandbox = Git{}

func (g Git) Materialize(ctx context.Context, request factory.MaterializationRequest) (factory.Materialization, error) {
	if request.TaskID == "" || request.Destination == "" {
		return factory.Materialization{}, fmt.Errorf("task identity and destination are required")
	}
	if g.Runner == nil {
		return factory.Materialization{}, fmt.Errorf("git runner unavailable")
	}
	var profile factorygit.Profile
	var err error
	switch request.SourceType {
	case "local":
		profile, err = factorygit.PrepareLocal(ctx, g.Runner, request.Source, request.Destination)
		if err != nil {
			return factory.Materialization{}, fmt.Errorf("materialize local repository: %w", err)
		}
	case "github":
		profile, err = factorygit.PrepareGitHub(ctx, g.Runner, request.Source, request.Destination)
		if err != nil {
			return factory.Materialization{}, fmt.Errorf("materialize github repository: %w", err)
		}
	default:
		return factory.Materialization{}, fmt.Errorf("unsupported repository source type %q", request.SourceType)
	}
	return fromProfile(profile), nil
}

func (g Git) Cleanup(ctx context.Context, request factory.CleanupRequest) error {
	if request.TaskID == "" {
		return fmt.Errorf("task identity is required")
	}
	if g.Runner == nil {
		return fmt.Errorf("git runner unavailable")
	}
	if request.SourceType != "local" || request.CanonicalPath == "" || request.WorkingPath == "" {
		return nil
	}
	if !ownedWorkingPath(request.WorkspaceRoot, request.WorkingPath) {
		return fmt.Errorf("repository sandbox is not owned by task")
	}
	if _, err := g.Runner.Run(ctx, "git", "-C", request.CanonicalPath, "worktree", "remove", "--force", request.WorkingPath); err != nil {
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

func fromProfile(profile factorygit.Profile) factory.Materialization {
	checks := make([]factory.Check, len(profile.Checks))
	for index, check := range profile.Checks {
		checks[index] = factory.Check{ID: check.ID, Command: check.Command}
	}
	return factory.Materialization{Root: profile.Root, SourceType: profile.SourceType, Source: profile.Source, BaseSHA: profile.BaseSHA, BranchName: profile.BranchName, Checks: checks, Generated: profile.Generated, Protected: profile.Protected, Tests: profile.Tests, PreChangeVerification: profile.PreChangeVerification, Instructions: profile.Instructions}
}

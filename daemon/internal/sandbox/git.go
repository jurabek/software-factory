package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/factory"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
)

// Git materializes task repositories using Git worktrees and GitHub clones.
type Git struct {
	Runner factorygit.Runner
}

var _ factory.Sandbox = Git{}

func (g Git) Materialize(ctx context.Context, request factory.MaterializationRequest) (factory.Materialization, error) {
	if request.TaskID == "" || request.RepositoryID == "" || request.Name == "" {
		return factory.Materialization{}, fmt.Errorf("task and repository identity are required")
	}
	if filepath.Base(filepath.Clean(request.Destination)) != request.Name {
		return factory.Materialization{}, fmt.Errorf("repository destination does not match repository identity")
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
	for _, repository := range request.Repositories {
		if repository.RepositoryID == "" || repository.Name == "" {
			return fmt.Errorf("repository identity is required")
		}
		if repository.SourceType != "local" || repository.CanonicalPath == "" || repository.WorkingPath == "" {
			continue
		}
		if !ownedWorkingPath(request.WorkspaceRoot, repository.WorkingPath, repository.Name) {
			return fmt.Errorf("repository sandbox is not owned by task")
		}
		if _, err := os.Lstat(repository.WorkingPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect repository sandbox: %w", err)
		}
		if _, err := g.Runner.Run(ctx, "git", "-C", repository.CanonicalPath, "worktree", "remove", "--force", repository.WorkingPath); err != nil {
			return fmt.Errorf("remove repository sandbox: %w", err)
		}
	}
	return nil
}

func ownedWorkingPath(workspaceRoot, workingPath, name string) bool {
	if workspaceRoot == "" || name == "" {
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
	return relative == filepath.Join("workspace", "repositories", name) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func fromProfile(profile factorygit.Profile) factory.Materialization {
	checks := make([]factory.Check, len(profile.Checks))
	for index, check := range profile.Checks {
		checks[index] = factory.Check{ID: check.ID, Command: check.Command}
	}
	return factory.Materialization{Root: profile.Root, SourceType: profile.SourceType, Source: profile.Source, BaseSHA: profile.BaseSHA, BranchName: profile.BranchName, Checks: checks, Generated: profile.Generated, Protected: profile.Protected, Tests: profile.Tests, PreChangeVerification: profile.PreChangeVerification, Instructions: profile.Instructions}
}

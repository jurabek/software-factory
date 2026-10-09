// Package builder owns implementation-stage entry and its typed plan handoff.
package builder

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type TestChange struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Result struct {
	stage.Common
	ChangedFiles  []string     `json:"changed_files"`
	CommitMessage string       `json:"commit_message"`
	TestChanges   []TestChange `json:"test_changes"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, stage.CommonFields...), "changed_files", "commit_message", "test_changes")
	if err := stage.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := stage.ValidateCommon(value.Common); err != nil {
		return value, err
	}
	if value.ChangedFiles == nil {
		return value, fmt.Errorf("builder changed_files array is required")
	}
	if value.TestChanges == nil {
		return value, fmt.Errorf("builder test_changes array is required")
	}
	seen := make(map[string]struct{}, len(value.TestChanges))
	for _, change := range value.TestChanges {
		if strings.TrimSpace(change.Path) == "" || strings.TrimSpace(change.Reason) == "" {
			return value, fmt.Errorf("builder test_changes entries require path and reason")
		}
		if filepath.IsAbs(change.Path) || strings.HasPrefix(change.Path, ":") {
			return value, fmt.Errorf("builder test change path must be relative: %q", change.Path)
		}
		clean := filepath.Clean(change.Path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return value, fmt.Errorf("builder test change path escapes root: %q", change.Path)
		}
		if filepath.ToSlash(clean) != change.Path {
			return value, fmt.Errorf("builder test change path must be canonical: %q", change.Path)
		}
		key := filepath.ToSlash(clean)
		if _, exists := seen[key]; exists {
			return value, fmt.Errorf("duplicate builder test change %q", key)
		}
		seen[key] = struct{}{}
	}
	return value, nil
}

func Instructions() string {
	return `Return exactly one JSON object: {` + stage.CommonInstructions() + `,"changed_files":[],"commit_message":"...","test_changes":[{"path":"...","reason":"..."}]}. Put the human-readable report in report_markdown.`
}

type service struct {
	kit   *stagekit.Kit
	tasks *task.Service
}

func New(kit *stagekit.Kit, tasks *task.Service) service { return service{kit: kit, tasks: tasks} }

// Build resumes a durable result when present, otherwise renders
// implementation prompts from the exact upstream plan, runs the build turn
// with Git-derived test evidence validation, and publishes the payload.
func (s service) Build(ctx context.Context, input stage.Input, plan stage.PlanResult) (stage.BuildResult, error) {
	return s.kit.RunAgentStage(ctx, input.TaskID, stagekit.AgentStage[stage.BuildResult]{
		Entry: stagekit.Entry{Kind: "build", Requires: []stagekit.Upstream{{Kind: "plan", AttemptID: plan.AttemptID}}, Enter: stagekit.Building},
		After: plan.AttemptID,
		Role:  "build", Instructions: Instructions(),
		Data: func(_ context.Context, task store.Task) (map[string]any, error) {
			if _, err := workspace.ReadProfile(task); err != nil {
				return nil, err
			}
			return map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath, "Plan": plan.Payload}, nil
		},
		Validate: func(task store.Task, text string) (any, error) {
			profile, err := workspace.ReadProfile(task)
			if err != nil {
				return nil, err
			}
			return ValidateWithEvidence(task.RepositoryPath, workspace.ReviewBase(task), profile.Tests, text)
		},
		OnValid: func(ctx context.Context, task store.Task, phase store.Phase, payload string) error {
			return PersistEvidence(ctx, s.kit, task, phase, payload)
		},
		Finalize: func(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult) (stagekit.Completion, error) {
			profile, err := workspace.ReadProfile(task)
			if err != nil {
				return stagekit.Completion{}, err
			}
			if err = CheckProtectedPaths(task.RepositoryPath, workspace.ReviewBase(task), profile.Protected); err != nil {
				return stagekit.Completion{}, err
			}
			if err = PersistEvidence(ctx, s.kit, task, phase, turn.Payload); err != nil {
				return stagekit.Completion{}, err
			}
			return stagekit.Completion{To: stagekit.Checking, Status: "success"}, nil
		},
		Result: func(_ store.Task, phase store.Phase, payload string) (stage.BuildResult, error) {
			return stage.BuildResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot}, nil
		},
	})
}

// ValidateWithEvidence validates the envelope and requires test_changes to
// match exactly the Git-derived changed tests.
func ValidateWithEvidence(repoPath, base string, tests []string, text string) (Result, error) {
	build, err := Validate(text)
	if err != nil {
		return build, err
	}
	expected, err := ChangedTestSet(repoPath, base, tests)
	if err != nil {
		return build, err
	}
	if err = ValidateTestChangeSet(build.TestChanges, expected); err != nil {
		return build, err
	}
	return build, nil
}

// ChangedTestSet returns the Git-derived changed tests for a base.
func ChangedTestSet(repoPath, base string, tests []string) (map[string]factorygit.Change, error) {
	entries, err := factorygit.ChangedTests(repoPath, base, tests)
	if err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	expected := make(map[string]factorygit.Change, len(entries))
	for _, entry := range entries {
		expected[entry.Path] = entry
	}
	return expected, nil
}

func ValidateTestChangeSet(changes []TestChange, expected map[string]factorygit.Change) error {
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		path := filepath.ToSlash(filepath.Clean(change.Path))
		key := path
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("test_changes entry is not a Git-derived changed test: %s", change.Path)
		}
		if path != change.Path {
			return fmt.Errorf("test_changes path is not canonical: %q", change.Path)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate test_changes entry %q", key)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(expected) {
		missing := make([]string, 0, len(expected)-len(seen))
		for key := range expected {
			if _, ok := seen[key]; !ok {
				missing = append(missing, key)
			}
		}
		return fmt.Errorf("test_changes is missing Git-derived changed tests: %s", strings.Join(missing, ", "))
	}
	return nil
}

// EvidenceSaver records test evidence for an attempt.
type EvidenceSaver interface {
	SaveTestChanges(context.Context, []store.TestChange) error
}

// PersistEvidence validates test evidence against Git-derived changes and
// stores it for the attempt.
func PersistEvidence(ctx context.Context, saver EvidenceSaver, task store.Task, phase store.Phase, payload string) error {
	build, err := Validate(payload)
	if err != nil {
		return err
	}
	profile, err := workspace.ReadProfile(task)
	if err != nil {
		return err
	}
	expected, err := ChangedTestSet(task.RepositoryPath, workspace.ReviewBase(task), profile.Tests)
	if err != nil {
		return err
	}
	if err = ValidateTestChangeSet(build.TestChanges, expected); err != nil {
		return err
	}
	changes := make([]store.TestChange, 0, len(build.TestChanges))
	for _, change := range build.TestChanges {
		value := expected[change.Path]
		changes = append(changes, store.TestChange{
			ID:         uuid.New().String(),
			TaskID:     task.ID,
			PhaseID:    phase.ID,
			Attempt:    phase.Attempt,
			Path:       change.Path,
			Reason:     change.Reason,
			ChangeKind: value.Kind,
			RenameFrom: value.RenameFrom,
			RenameTo:   value.RenameTo,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	return saver.SaveTestChanges(ctx, changes)
}

// CheckProtectedPaths rejects builds touching protected paths.
func CheckProtectedPaths(repoPath, base string, protected []string) error {
	files, err := factorygit.ChangedFiles(repoPath, base)
	if err != nil {
		return err
	}
	for _, file := range files {
		if factorygit.MatchesPath(file, protected) {
			return fmt.Errorf("builder changed protected path %s", file)
		}
	}
	return nil
}

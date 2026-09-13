// Package builder owns implementation-stage entry and its typed plan handoff.
package builder

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type TestChange struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Result struct {
	agentexec.Common
	ChangedFiles  []string     `json:"changed_files"`
	CommitMessage string       `json:"commit_message"`
	TestChanges   []TestChange `json:"test_changes"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, agentexec.CommonFields...), "changed_files", "commit_message", "test_changes")
	if err := agentexec.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := agentexec.ValidateCommon(value.Common); err != nil {
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
	return `Return exactly one JSON object: {` + agentexec.CommonInstructions() + `,"changed_files":[],"commit_message":"...","test_changes":[{"path":"...","reason":"..."}]}. Put the human-readable report in report_markdown.`
}

// Publisher is the narrow build-scoped bridge into orchestration. It begins
// the build phase, publishes the final payload atomically with message
// synchronization, and resolves durable resume state. Checkpoint mechanics
// move to Pipeline later; the bridge stays build-scoped.
type Publisher interface {
	SavedBuild(ctx context.Context, taskID, planAttemptID string) (pipeline.BuildResult, bool, error)
	BeginBuild(ctx context.Context, taskID, planAttemptID string) (store.Task, store.Phase, error)
	PublishBuild(ctx context.Context, task store.Task, phase store.Phase, payload string) (pipeline.BuildResult, error)
	FailBuild(ctx context.Context, phase store.Phase, cause error)
}

// EvidenceStore persists builder test evidence.
type EvidenceStore interface {
	SaveTestChanges(ctx context.Context, changes []store.TestChange) error
}

// Deps supplies prompt configuration, turn execution, and build publication.
type Deps struct {
	Turner     agentexec.Deps
	Sinks      agentexec.SinkFactory
	Configurer agentexec.Configurer
	Publisher  Publisher
}

type Service struct{ deps Deps }

func New(deps Deps) Service { return Service{deps: deps} }

// Build resumes a durable result when present, otherwise renders
// implementation prompts from the exact upstream plan, runs the build turn
// with Git-derived test evidence validation, and publishes the payload.
func (s Service) Build(ctx context.Context, input pipeline.Input, plan pipeline.PlanResult) (pipeline.BuildResult, error) {
	if result, ok, err := s.deps.Publisher.SavedBuild(ctx, input.TaskID, plan.AttemptID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.deps.Publisher.BeginBuild(ctx, input.TaskID, plan.AttemptID)
	if err != nil {
		return pipeline.BuildResult{}, err
	}
	configured, err := s.deps.Configurer.TaskConfig(ctx, task)
	if err != nil {
		s.deps.Publisher.FailBuild(ctx, phase, err)
		return pipeline.BuildResult{}, err
	}
	agent, ok := configured.Config.Agent(phase.Owner)
	if !ok {
		err = fmt.Errorf("agent %s not configured", phase.Owner)
		s.deps.Publisher.FailBuild(ctx, phase, err)
		return pipeline.BuildResult{}, err
	}
	profile, err := workspace.ReadProfile(task)
	if err != nil {
		s.deps.Publisher.FailBuild(ctx, phase, err)
		return pipeline.BuildResult{}, err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath, "Plan": plan.Payload}
	systemPrompt, userPrompt, err := agentexec.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		filepath.Join(configured.TaskDir, "prompts", agent.Name),
		Instructions(),
	)
	if err != nil {
		s.deps.Publisher.FailBuild(ctx, phase, err)
		return pipeline.BuildResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := s.deps.Turner
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	validate := func(text string) (any, error) {
		return ValidateWithEvidence(ctx, turner.Git, task.RepositoryPath, workspace.ReviewBase(task), profile.Tests, text)
	}
	payload, err := agentexec.RunTurn(ctx, turner, agentexec.TurnInput{
		TaskID: task.ID, Phase: phase, Role: phase.Name,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath: task.RepositoryPath,
		SessionDir: filepath.Join(configured.TaskDir, "sessions", phase.Name, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: readOnly(phase), EnvelopeKind: "build", CorrectionSuffix: Instructions(),
		Validate: validate,
		Sink:     s.deps.Sinks(task.ID, phase.ID, harnessName),
	})
	if err != nil {
		s.deps.Publisher.FailBuild(ctx, phase, err)
		return pipeline.BuildResult{}, err
	}
	return s.deps.Publisher.PublishBuild(ctx, task, phase, payload)
}

func readOnly(phase store.Phase) bool {
	return phase.Kind == "review" || phase.Owner == "planner" || phase.Owner == "reviewer"
}

// ValidateWithEvidence validates the envelope and requires test_changes to
// match exactly the Git-derived changed tests.
func ValidateWithEvidence(ctx context.Context, git factorygit.Runner, repoPath, base string, tests []string, text string) (Result, error) {
	build, err := Validate(text)
	if err != nil {
		return build, err
	}
	expected, err := ChangedTestSet(ctx, git, repoPath, base, tests)
	if err != nil {
		return build, err
	}
	if err = ValidateTestChangeSet(build.TestChanges, expected); err != nil {
		return build, err
	}
	return build, nil
}

// ChangedTestSet returns the Git-derived changed tests for a base.
func ChangedTestSet(ctx context.Context, git factorygit.Runner, repoPath, base string, tests []string) (map[string]factorygit.Change, error) {
	entries, err := factorygit.ChangedEntries(ctx, git, repoPath, base)
	if err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	expected := make(map[string]factorygit.Change)
	for _, entry := range entries {
		if factorygit.MatchesGlob(entry.Path, tests) {
			expected[entry.Path] = entry
		}
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

// PersistEvidence validates test evidence against Git-derived changes and
// stores it for the attempt.
func PersistEvidence(ctx context.Context, git factorygit.Runner, db EvidenceStore, task store.Task, phase store.Phase, payload string) error {
	build, err := Validate(payload)
	if err != nil {
		return err
	}
	profile, err := workspace.ReadProfile(task)
	if err != nil {
		return err
	}
	expected, err := ChangedTestSet(ctx, git, task.RepositoryPath, workspace.ReviewBase(task), profile.Tests)
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
	return db.SaveTestChanges(ctx, changes)
}

// CheckProtectedPaths rejects builds touching protected paths.
func CheckProtectedPaths(ctx context.Context, git factorygit.Runner, repoPath, base string, protected []string) error {
	files, err := factorygit.ChangedFiles(ctx, git, repoPath, base)
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

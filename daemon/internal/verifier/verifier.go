// Package verifier owns deterministic verification-stage entry.
package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// CheckStore is the narrow persistence surface for check execution.
type CheckStore interface {
	SaveCheck(context.Context, store.Check) error
	StartProcess(context.Context, string, string, string, string, int, string) (int64, error)
	EndProcess(context.Context, string, int, int) error
	Checks(context.Context, string) ([]store.Check, error)
	Comparisons(context.Context, string) ([]store.Comparison, error)
	SaveComparison(context.Context, store.Comparison) error
	Phases(context.Context, string) ([]store.Phase, error)
}

// Snapshots materializes baseline trees for comparison runs.
type Snapshots interface {
	MaterializeScratch(context.Context, store.Task, string, string) error
}

// Publisher is the narrow verification-scoped bridge into orchestration. It
// begins the verify phase, publishes the final report atomically, and
// resolves durable resume state. Checkpoint mechanics move to Pipeline later;
// the bridge stays verification-scoped.
type Publisher interface {
	SavedVerification(ctx context.Context, taskID, buildAttemptID string) (pipeline.VerificationResult, bool, error)
	BeginVerification(ctx context.Context, taskID, planAttemptID, buildAttemptID string) (store.Task, store.Phase, error)
	PublishVerification(ctx context.Context, task store.Task, phase store.Phase, checks []store.Check, comparisons []store.Comparison, report string) (pipeline.VerificationResult, error)
	FailVerification(ctx context.Context, phase store.Phase, cause error)
}

// Deps supplies check persistence, git, scratch materialization, and verify
// publication.
type Deps struct {
	Checks    CheckStore
	Git       factorygit.Runner
	Snapshots Snapshots
	Publisher Publisher
}

type Service struct{ deps Deps }

func New(deps Deps) Service { return Service{deps: deps} }

// Verify resumes a durable result when present, otherwise runs primary checks
// and baseline/test-overlay comparisons, then publishes the report.
func (s Service) Verify(ctx context.Context, input pipeline.Input, plan pipeline.PlanResult, build pipeline.BuildResult) (pipeline.VerificationResult, error) {
	if result, ok, err := s.deps.Publisher.SavedVerification(ctx, input.TaskID, build.AttemptID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.deps.Publisher.BeginVerification(ctx, input.TaskID, plan.AttemptID, build.AttemptID)
	if err != nil {
		return pipeline.VerificationResult{}, err
	}
	profile, err := workspace.ReadProfile(task)
	if err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	if err = s.runChecks(ctx, task, phase, profile.Checks, "primary", ""); err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	if err = s.runComparisons(ctx, task, phase, profile); err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	checks, err := s.deps.Checks.Checks(ctx, task.ID)
	if err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	phaseChecks := make([]store.Check, 0)
	for _, check := range checks {
		if check.PhaseID == phase.ID {
			phaseChecks = append(phaseChecks, check)
		}
	}
	comparisons, err := s.deps.Checks.Comparisons(ctx, task.ID)
	if err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	phaseComparisons := make([]store.Comparison, 0)
	for _, comparison := range comparisons {
		if comparison.PhaseID == phase.ID {
			phaseComparisons = append(phaseComparisons, comparison)
		}
	}
	report, err := Report(phaseChecks)
	if err != nil {
		s.deps.Publisher.FailVerification(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	return s.deps.Publisher.PublishVerification(ctx, task, phase, phaseChecks, phaseComparisons, report)
}

// Report renders the deterministic verification report from phase checks.
func Report(checks []store.Check) (string, error) {
	var report strings.Builder
	report.WriteString("# Verification\n\n")
	for _, check := range checks {
		status := "passed"
		if check.Status != "passed" {
			status = "failed"
		}
		fmt.Fprintf(&report, "- **%s**: %s (`%s`)\n", check.Name, status, check.Command)
	}
	return report.String(), nil
}

// RunChecks executes declared checks for legacy retry paths that manage
// their own phase lifecycle.
func (s Service) RunChecks(ctx context.Context, task store.Task, phase store.Phase, checks []workspace.Check, checkPhase, baseline string) error {
	return s.runChecks(ctx, task, phase, checks, checkPhase, baseline)
}

// RunComparisons executes baseline/test-overlay comparisons for legacy retry
// paths that manage their own phase lifecycle.
func (s Service) RunComparisons(ctx context.Context, task store.Task, phase store.Phase, profile workspace.Materialization) error {
	return s.runComparisons(ctx, task, phase, profile)
}

type checkRunError struct {
	kind string
	err  error
}

func (e *checkRunError) Error() string { return e.err.Error() }
func (e *checkRunError) Unwrap() error { return e.err }

func (s Service) runChecks(ctx context.Context, task store.Task, phase store.Phase, checks []workspace.Check, checkPhase, baseline string) error {
	return s.runChecksAt(ctx, task, phase, task.RepositoryPath, checks, checkPhase, baseline)
}

func (s Service) runChecksAt(ctx context.Context, task store.Task, phase store.Phase, workingPath string, checks []workspace.Check, checkPhase, baseline string) error {
	var firstErr error
	for index, declared := range checks {
		check, err := s.runCheck(ctx, task, phase, workingPath, declared, index, checkPhase, baseline)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if check.Status == "cancelled" || (err != nil && check.Status == "inconclusive") {
			return err
		}
	}
	return firstErr
}

func (s Service) runCheck(ctx context.Context, task store.Task, phase store.Phase, workingPath string, declared workspace.Check, index int, checkPhase, baseline string) (store.Check, error) {
	started := time.Now().UTC()
	check := store.Check{
		ID:                 fmt.Sprintf("%s-%s-%s-%d", phase.ID, checkPhase, safeFileName(declared.ID), index),
		TaskID:             task.ID,
		PhaseID:            phase.ID,
		StageID:            phase.Name,
		Phase:              checkPhase,
		ComparisonBaseline: baseline,
		Name:               declared.ID,
		Command:            declared.Command,
		Attempt:            phase.Attempt,
		ExitCode:           -1,
		StartedAt:          started.Format(time.RFC3339Nano),
	}
	logPath := filepath.Join(task.WorkspacePath, "attempts", fmt.Sprintf("%d-%s", phase.Attempt, phase.ID), "checks", checkPhase, safeFileName(declared.ID)+".log")
	check.ArtifactPath = logPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return check, err
	}
	capture := &tailCapture{limit: maxCapturedOutput}
	command := exec.Command("/bin/sh", "-c", declared.Command)
	command.Dir = workingPath
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = capture
	command.Stderr = capture
	if err := command.Start(); err != nil {
		check.Status = "inconclusive"
		check.Output = err.Error()
		check.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		check.DurationMS = int(time.Since(started).Milliseconds())
		if saveErr := s.deps.Checks.SaveCheck(context.WithoutCancel(ctx), check); saveErr != nil {
			return check, saveErr
		}
		return check, &checkRunError{kind: "inconclusive", err: fmt.Errorf("start check %s: %w", declared.ID, err)}
	}
	pid := command.Process.Pid
	if _, err := s.deps.Checks.StartProcess(context.WithoutCancel(ctx), task.ID, phase.ID, "check", declared.ID, pid, declared.Command); err != nil {
		terminateProcessGroup(pid)
		_, _ = command.Wait(), s.deps.Checks.EndProcess(context.WithoutCancel(ctx), task.ID, pid, -1)
		return check, err
	}
	waitErr, cancelled := waitForProcess(ctx, command)
	check.ExitCode = processExitCode(waitErr)
	check.Output = capture.String()
	if cancelled || errors.Is(ctx.Err(), context.Canceled) {
		check.Status = "cancelled"
	} else if waitErr != nil {
		check.Status = "failed"
		if checkSetupFailure(check.ExitCode, check.Output) {
			check.Status = "inconclusive"
		}
	} else {
		check.Status = "passed"
	}
	check.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	check.DurationMS = int(time.Since(started).Milliseconds())
	if err := os.WriteFile(logPath, []byte(check.Output), 0o600); err != nil {
		return check, err
	}
	endErr := s.deps.Checks.EndProcess(context.WithoutCancel(ctx), task.ID, pid, check.ExitCode)
	saveErr := s.deps.Checks.SaveCheck(context.WithoutCancel(ctx), check)
	if endErr != nil {
		return check, endErr
	}
	if saveErr != nil {
		return check, saveErr
	}
	if check.Status == "cancelled" {
		return check, &checkRunError{kind: "cancelled", err: ctx.Err()}
	}
	if check.Status == "failed" {
		return check, &checkRunError{kind: "failed", err: fmt.Errorf("check %s exited %d", declared.ID, check.ExitCode)}
	}
	if check.Status == "inconclusive" {
		return check, &checkRunError{kind: "inconclusive", err: fmt.Errorf("check %s could not establish a runnable environment", declared.ID)}
	}
	return check, nil
}

func checkSetupFailure(exitCode int, output string) bool {
	if exitCode == 126 || exitCode == 127 {
		return true
	}
	lower := strings.ToLower(output)
	for _, marker := range []string{"command not found", "no such file or directory", "module not found", "could not resolve"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func waitForProcess(ctx context.Context, command *exec.Cmd) (error, bool) {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err, false
	case <-ctx.Done():
		terminateProcessGroup(command.Process.Pid)
		select {
		case err := <-done:
			return err, true
		case <-time.After(250 * time.Millisecond):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			return <-done, true
		}
	}
}

func terminateProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unnamed"
	}
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func (s Service) runComparisons(ctx context.Context, task store.Task, phase store.Phase, profile workspace.Materialization) error {
	baseline, err := s.comparisonBaseline(ctx, task, phase)
	if err != nil {
		return err
	}
	started := time.Now()
	comparison := store.Comparison{ID: randomID(), TaskID: task.ID, PhaseID: phase.ID, Attempt: phase.Attempt, BaselineSnapshot: baseline, OverlayPaths: []string{}, CreatedAt: nowString()}
	if !profile.PreChangeVerification {
		comparison.Status, comparison.Reason = "skipped", "pre-change verification disabled"
		comparison.DurationMS = int(time.Since(started).Milliseconds())
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return nil
	}
	if len(profile.Checks) == 0 {
		comparison.Status, comparison.Reason = "skipped", "repository has no declared checks"
		comparison.DurationMS = int(time.Since(started).Milliseconds())
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return nil
	}
	entries, entriesErr := ChangedTestEntries(ctx, s.deps.Git, task.RepositoryPath, workspace.ReviewBase(task), profile.Tests)
	if entriesErr != nil {
		comparison.Status, comparison.Reason = "inconclusive", entriesErr.Error()
		comparison.DurationMS = int(time.Since(started).Milliseconds())
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return nil
	}
	comparison.OverlayPaths = ComparisonPaths(entries)
	if len(comparison.OverlayPaths) == 0 {
		comparison.Status, comparison.Reason = "skipped", "no added or modified tests"
		comparison.DurationMS = int(time.Since(started).Milliseconds())
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return nil
	}
	if baseline == "" {
		comparison.Status, comparison.Reason = "inconclusive", "pre-implementation snapshot is unavailable"
		comparison.DurationMS = int(time.Since(started).Milliseconds())
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return nil
	}
	if rootErr := os.MkdirAll(filepath.Join(task.WorkspacePath, "workspace"), 0o700); rootErr != nil {
		return rootErr
	}
	comparisonRoot, rootErr := os.MkdirTemp(filepath.Join(task.WorkspacePath, "workspace"), "comparison-")
	if rootErr != nil {
		comparison.Status, comparison.Reason = "inconclusive", rootErr.Error()
		if err = s.saveComparison(ctx, &comparison); err != nil {
			return err
		}
		return rootErr
	}
	func() {
		defer os.RemoveAll(comparisonRoot)
		baselineRoot := filepath.Join(comparisonRoot, "baseline")
		if materializeErr := s.deps.Snapshots.MaterializeScratch(ctx, task, baseline, baselineRoot); materializeErr != nil {
			comparison.Status, comparison.Reason = "inconclusive", materializeErr.Error()
			return
		}
		if checkErr := s.runChecksAt(ctx, task, phase, baselineRoot, profile.Checks, "baseline", baseline); checkErr != nil {
			comparison.Status, comparison.Reason = "inconclusive", checkErr.Error()
			if runErr, ok := checkErr.(*checkRunError); ok && runErr.kind == "cancelled" {
				comparison.Status = "cancelled"
			}
			return
		}
		overlayRoot := filepath.Join(comparisonRoot, "overlay")
		if materializeErr := s.deps.Snapshots.MaterializeScratch(ctx, task, baseline, overlayRoot); materializeErr != nil {
			comparison.Status, comparison.Reason = "inconclusive", materializeErr.Error()
			return
		}
		for _, path := range comparison.OverlayPaths {
			if copyErr := CopyOverlayPath(task.RepositoryPath, overlayRoot, path); copyErr != nil {
				comparison.Status, comparison.Reason = "inconclusive", copyErr.Error()
				return
			}
		}
		checkErr := s.runChecksAt(ctx, task, phase, overlayRoot, profile.Checks, "test_overlay", baseline)
		switch {
		case checkErr == nil:
			comparison.Status, comparison.Reason = "overlay_checks_passed", "all overlay checks passed"
		case errors.Is(checkErr, context.Canceled):
			comparison.Status, comparison.Reason = "cancelled", checkErr.Error()
		case func() bool { value, ok := checkErr.(*checkRunError); return ok && value.kind == "failed" }():
			comparison.Status, comparison.Reason = "overlay_checks_failed", checkErr.Error()
		default:
			comparison.Status, comparison.Reason = "inconclusive", checkErr.Error()
		}
	}()
	if ctx.Err() != nil {
		comparison.Status, comparison.Reason = "cancelled", ctx.Err().Error()
	}
	comparison.DurationMS = int(time.Since(started).Milliseconds())
	if err = s.saveComparison(ctx, &comparison); err != nil {
		return err
	}
	if comparison.Status == "cancelled" {
		return context.Canceled
	}
	return nil
}

func (s Service) saveComparison(ctx context.Context, comparison *store.Comparison) error {
	if ctx.Err() != nil {
		comparison.Status = "cancelled"
		comparison.Reason = ctx.Err().Error()
	}
	return s.deps.Checks.SaveComparison(context.WithoutCancel(ctx), *comparison)
}

// ExpectedTestChange is a Git-derived changed test entry.
type ExpectedTestChange struct {
	Change factorygit.Change
}

// ChangedTestEntries returns Git-derived changed tests for a base.
func ChangedTestEntries(ctx context.Context, git factorygit.Runner, repoPath, base string, tests []string) ([]ExpectedTestChange, error) {
	entries, err := factorygit.ChangedEntries(ctx, git, repoPath, base)
	if err != nil {
		return nil, err
	}
	result := make([]ExpectedTestChange, 0)
	for _, entry := range entries {
		if factorygit.MatchesGlob(entry.Path, tests) {
			result = append(result, ExpectedTestChange{Change: entry})
		}
	}
	return result, nil
}

// ComparisonPaths resolves overlay paths from changed test entries.
func ComparisonPaths(entries []ExpectedTestChange) []string {
	paths := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		change := entry.Change
		if change.Kind == "deleted" || change.Path == change.RenameFrom {
			continue
		}
		if !seen[change.Path] {
			seen[change.Path] = true
			paths = append(paths, change.Path)
		}
	}
	return paths
}

func (s Service) comparisonBaseline(ctx context.Context, task store.Task, current store.Phase) (string, error) {
	phases, err := s.deps.Checks.Phases(ctx, task.ID)
	if err != nil {
		return "", err
	}
	for index := len(phases) - 1; index >= 0; index-- {
		candidate := phases[index]
		if candidate.Kind != "build" || candidate.InputSnapshot == "" || candidate.Superseded {
			continue
		}
		if current.BranchID == "" || candidate.BranchID == current.BranchID {
			return candidate.InputSnapshot, nil
		}
	}
	for index := len(phases) - 1; index >= 0; index-- {
		candidate := phases[index]
		if candidate.Kind == "build" && candidate.InputSnapshot != "" && !candidate.Superseded {
			return candidate.InputSnapshot, nil
		}
	}
	return "", nil
}

// CopyOverlayPath copies a repository-relative path into an overlay root.
func CopyOverlayPath(sourceRoot, destinationRoot, relative string) error {
	clean := filepath.Clean(relative)
	if relative == "" || filepath.IsAbs(relative) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("overlay path escapes repository: %q", relative)
	}
	source := filepath.Join(sourceRoot, clean)
	destination := filepath.Join(destinationRoot, clean)
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("read overlay path %s: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolvedRoot, rootErr := filepath.EvalSymlinks(sourceRoot)
		resolvedPath, pathErr := filepath.EvalSymlinks(source)
		if rootErr != nil || pathErr != nil || !withinPath(resolvedRoot, resolvedPath) {
			return fmt.Errorf("overlay symlink escapes repository: %s", relative)
		}
		link, readErr := os.Readlink(source)
		if readErr != nil {
			return readErr
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		_ = os.Remove(destination)
		return os.Symlink(link, destination)
	}
	if info.IsDir() {
		return fmt.Errorf("overlay path is a directory: %s", relative)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err = os.WriteFile(destination, body, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}

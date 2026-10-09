// Package verifier owns deterministic verification-stage entry.
package verifier

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/checkrun"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

func randomID() string {
	var bytes [12]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func withinPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type service struct {
	kit   *stagekit.Kit
	tasks *task.Service
}

func New(kit *stagekit.Kit, tasks *task.Service) service { return service{kit: kit, tasks: tasks} }

// Verify resumes a durable result when present, otherwise runs primary checks
// and baseline/test-overlay comparisons, then publishes the report.
func (s service) Verify(ctx context.Context, input stage.Input, plan stage.PlanResult, build stage.BuildResult) (stage.VerificationResult, error) {
	if result, ok, err := s.savedVerification(ctx, input.TaskID, build.AttemptID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.kit.BeginStage(ctx, input.TaskID, stagekit.Entry{Kind: "verify", Requires: []stagekit.Upstream{{Kind: "plan", AttemptID: plan.AttemptID}, {Kind: "build", AttemptID: build.AttemptID}}, Enter: stagekit.Checking})
	if err != nil {
		return stage.VerificationResult{}, err
	}
	profile, err := workspace.ReadProfile(task)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	if err = s.runChecks(ctx, task, phase, profile.Checks, "primary", ""); err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	if err = s.runComparisons(ctx, task, phase, profile); err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	checks, err := s.kit.Checks(ctx, task.ID)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	phaseChecks := make([]store.Check, 0)
	for _, check := range checks {
		if check.PhaseID == phase.ID {
			phaseChecks = append(phaseChecks, check)
		}
	}
	comparisons, err := s.kit.Comparisons(ctx, task.ID)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	phaseComparisons := make([]store.Comparison, 0)
	for _, comparison := range comparisons {
		if comparison.PhaseID == phase.ID {
			phaseComparisons = append(phaseComparisons, comparison)
		}
	}
	report, err := Report(phaseChecks)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	passed := true
	for _, check := range phaseChecks {
		if check.Status != "passed" {
			passed = false
		}
	}
	return s.publishVerification(ctx, phase, phaseChecks, phaseComparisons, report, passed)
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

type checkRunError struct {
	kind string
	err  error
}

func (e *checkRunError) Error() string { return e.err.Error() }
func (e *checkRunError) Unwrap() error { return e.err }

func (s service) runChecks(ctx context.Context, task store.Task, phase store.Phase, checks []workspace.Check, checkPhase, baseline string) error {
	return s.runChecksAt(ctx, task, phase, task.RepositoryPath, checks, checkPhase, baseline)
}

func (s service) runChecksAt(ctx context.Context, task store.Task, phase store.Phase, workingPath string, checks []workspace.Check, checkPhase, baseline string) error {
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

func (s service) runCheck(ctx context.Context, task store.Task, phase store.Phase, workingPath string, declared workspace.Check, index int, checkPhase, baseline string) (store.Check, error) {
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
	check.OutputPath = logPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return check, err
	}
	run, err := checkrun.Run(ctx, checkrun.Check{Command: declared.Command, Dir: workingPath}, func(pid int) error {
		return s.kit.StartProcess(context.WithoutCancel(ctx), task.ID, phase.ID, "check", declared.ID, pid, declared.Command)
	})
	if err != nil {
		_ = s.kit.EndProcess(context.WithoutCancel(ctx), task.ID, run.PID, -1)
		return check, err
	}
	if run.StartErr != nil {
		check.Status = run.Status
		check.Output = run.Output
		check.EndedAt = run.EndedAt.Format(time.RFC3339Nano)
		check.DurationMS = int(run.EndedAt.Sub(started).Milliseconds())
		if saveErr := s.kit.SaveCheck(context.WithoutCancel(ctx), check); saveErr != nil {
			return check, saveErr
		}
		return check, &checkRunError{kind: "inconclusive", err: fmt.Errorf("start check %s: %w", declared.ID, run.StartErr)}
	}
	pid := run.PID
	check.ExitCode = run.ExitCode
	check.Output = run.Output
	check.Status = run.Status
	check.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	check.DurationMS = int(time.Since(started).Milliseconds())
	if err := os.WriteFile(logPath, []byte(check.Output), 0o600); err != nil {
		return check, err
	}
	endErr := s.kit.EndProcess(context.WithoutCancel(ctx), task.ID, pid, check.ExitCode)
	saveErr := s.kit.SaveCheck(context.WithoutCancel(ctx), check)
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

func (s service) runComparisons(ctx context.Context, task store.Task, phase store.Phase, profile workspace.Materialization) error {
	baseline, err := s.kit.BuildBaseline(ctx, task.ID)
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
	entries, entriesErr := factorygit.ChangedTests(task.RepositoryPath, workspace.ReviewBase(task), profile.Tests)
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
		if materializeErr := s.kit.MaterializeScratch(ctx, task, baseline, baselineRoot); materializeErr != nil {
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
		if materializeErr := s.kit.MaterializeScratch(ctx, task, baseline, overlayRoot); materializeErr != nil {
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

func (s service) saveComparison(ctx context.Context, comparison *store.Comparison) error {
	if ctx.Err() != nil {
		comparison.Status = "cancelled"
		comparison.Reason = ctx.Err().Error()
	}
	return s.kit.SaveComparison(context.WithoutCancel(ctx), *comparison)
}

// ComparisonPaths resolves overlay paths from changed test entries.
func ComparisonPaths(entries []factorygit.Change) []string {
	paths := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, change := range entries {
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

func (s service) savedVerification(ctx context.Context, taskID, buildAttemptID string) (stage.VerificationResult, bool, error) {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return stage.VerificationResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "verify")
	if err != nil {
		return stage.VerificationResult{}, false,
			err
	}
	phase, ok, err := s.kit.Attempts().Resume(ctx, task.ID, stageDef.ID, buildAttemptID)
	if err != nil || !ok {
		return stage.VerificationResult{}, false, err
	}
	checks, err := s.kit.Checks(ctx, task.ID)
	if err != nil {
		return stage.VerificationResult{},
			false, err
	}
	passed := true
	for _, check := range checks {
		if check.PhaseID == phase.ID && check.Status != "passed" {
			passed = false
		}
	}
	return stage.VerificationResult{
		AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Passed: passed,
	}, true, nil
}

func (s service) publishVerification(ctx context.Context, phase store.Phase, checks []store.Check, comparisons []store.Comparison, report string, passed bool) (stage.VerificationResult, error) {
	status, to := "success", stagekit.Reviewing
	if !passed {
		status, to = "failed", stagekit.Blocked
	}
	if err := s.kit.Complete(
		ctx, stagekit.Completion{
			Phase: phase,
			From:  stagekit.Checking, To: to, Status: status, Checks: checks, Comparisons: comparisons,
		}); err != nil {
		s.kit.Fail(ctx, phase,
			err)
		return stage.VerificationResult{}, err
	}
	return stage.VerificationResult{AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Report: report, Passed: passed}, nil
}

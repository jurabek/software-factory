package factory

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
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type expectedTestChange struct {
	repository store.TaskRepository
	change     factorygit.Change
}

func (s *qualityService) builderValidator(ctx context.Context, task store.Task, profiles map[string]Materialization) validator {
	return func(text string) (any, error) {
		build, err := ValidateBuild(text)
		if err != nil {
			return build, err
		}
		expected, err := s.changedTestSet(ctx, task, profiles)
		if err != nil {
			return build, err
		}
		if err = validateTestChangeSet(build.TestChanges, expected); err != nil {
			return build, err
		}
		return build, nil
	}
}

func (s *qualityService) changedTestSet(ctx context.Context, task store.Task, profiles map[string]Materialization) (map[string]expectedTestChange, error) {
	expected := make(map[string]expectedTestChange)
	for _, repository := range task.Repositories {
		profile, ok := profiles[repository.Name]
		if !ok {
			return nil, fmt.Errorf("repository profile %s is unavailable", repository.Name)
		}
		entries, err := factorygit.ChangedEntries(ctx, s.git, repository.WorkingPath, repositoryReviewBase(repository))
		if err != nil {
			return nil, fmt.Errorf("read changes for %s: %w", repository.Name, err)
		}
		for _, entry := range entries {
			if factorygit.MatchesGlob(entry.Path, profile.Tests) {
				expected[repository.ID+"\x00"+entry.Path] = expectedTestChange{repository: repository, change: entry}
			}
		}
	}
	return expected, nil
}

func validateTestChangeSet(changes []TestChange, expected map[string]expectedTestChange) error {
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		path := filepath.ToSlash(filepath.Clean(change.Path))
		key := change.RepositoryID + "\x00" + path
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("test_changes entry is not a Git-derived changed test: %s/%s", change.RepositoryID, change.Path)
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
		for key, value := range expected {
			if _, ok := seen[key]; !ok {
				missing = append(missing, value.repository.Name+"/"+value.change.Path)
			}
		}
		return fmt.Errorf("test_changes is missing Git-derived changed tests: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (s *qualityService) persistBuilderEvidence(ctx context.Context, task store.Task, phase store.Phase, payload string) error {
	changes, err := s.builderEvidence(ctx, task, phase, payload)
	if err != nil {
		return err
	}
	return s.db.SaveTestChanges(ctx, changes)
}

func (s *qualityService) builderEvidence(ctx context.Context, task store.Task, phase store.Phase, payload string) ([]store.TestChange, error) {
	build, err := ValidateBuild(payload)
	if err != nil {
		return nil, err
	}
	profiles, err := s.workspace.InspectProfiles(ctx, task)
	if err != nil {
		return nil, err
	}
	expected, err := s.changedTestSet(ctx, task, profiles)
	if err != nil {
		return nil, err
	}
	if err = validateTestChangeSet(build.TestChanges, expected); err != nil {
		return nil, err
	}
	changes := make([]store.TestChange, 0, len(build.TestChanges))
	for _, change := range build.TestChanges {
		value := expected[change.RepositoryID+"\x00"+change.Path]
		changes = append(changes, store.TestChange{
			ID:             randomID(),
			TaskID:         task.ID,
			PhaseID:        phase.ID,
			Attempt:        phase.Attempt,
			RepositoryID:   change.RepositoryID,
			RepositoryName: value.repository.Name,
			Path:           change.Path,
			Reason:         change.Reason,
			ChangeKind:     value.change.Kind,
			RenameFrom:     value.change.RenameFrom,
			RenameTo:       value.change.RenameTo,
			CreatedAt:      nowString(),
		})
	}
	return changes, nil
}

type checkRunError struct {
	kind string
	err  error
}

func (e *checkRunError) Error() string { return e.err.Error() }
func (e *checkRunError) Unwrap() error { return e.err }

func (s *qualityService) runChecks(ctx context.Context, task store.Task, phase store.Phase, repository store.TaskRepository, checks []Check, checkPhase, baseline string) error {
	return s.runChecksAt(ctx, task, phase, repository, repository.WorkingPath, checks, checkPhase, baseline)
}

func (s *qualityService) runChecksAt(ctx context.Context, task store.Task, phase store.Phase, repository store.TaskRepository, workingPath string, checks []Check, checkPhase, baseline string) error {
	var firstErr error
	for index, declared := range checks {
		check, err := s.runCheck(ctx, task, phase, repository, workingPath, declared, index, checkPhase, baseline)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if check.Status == "cancelled" || (err != nil && check.Status == "inconclusive") {
			return err
		}
	}
	return firstErr
}

func (s *qualityService) runCheck(ctx context.Context, task store.Task, phase store.Phase, repository store.TaskRepository, workingPath string, declared Check, index int, checkPhase, baseline string) (store.Check, error) {
	started := time.Now().UTC()
	check := store.Check{
		ID:                 fmt.Sprintf("%s-%s-%s-%d", phase.ID, checkPhase, safeFileName(declared.ID), index),
		TaskID:             task.ID,
		PhaseID:            phase.ID,
		RepositoryID:       repository.ID,
		StageID:            phase.Name,
		Phase:              checkPhase,
		ComparisonBaseline: baseline,
		Name:               declared.ID,
		Command:            declared.Command,
		Attempt:            phase.Attempt,
		ExitCode:           -1,
		StartedAt:          started.Format(time.RFC3339Nano),
	}
	logPath := filepath.Join(task.WorkspacePath, "attempts", fmt.Sprintf("%d-%s", phase.Attempt, phase.ID), "checks", checkPhase, safeFileName(repository.Name), safeFileName(declared.ID)+".log")
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
		if saveErr := s.db.SaveCheck(context.WithoutCancel(ctx), check); saveErr != nil {
			return check, saveErr
		}
		return check, &checkRunError{kind: "inconclusive", err: fmt.Errorf("start check %s: %w", declared.ID, err)}
	}
	pid := command.Process.Pid
	if _, err := s.db.StartProcess(context.WithoutCancel(ctx), task.ID, phase.ID, "check", declared.ID, pid, declared.Command); err != nil {
		terminateProcessGroup(pid)
		_, _ = command.Wait(), s.db.EndProcess(context.WithoutCancel(ctx), task.ID, pid, -1)
		return check, err
	}
	waitErr, cancelled, terminationErr := waitForProcess(ctx, command)
	check.ExitCode = processExitCode(waitErr)
	check.Output = capture.String()
	if terminationErr != nil {
		check.Status = "inconclusive"
	} else if cancelled || errors.Is(ctx.Err(), context.Canceled) {
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
	endErr := s.db.EndProcess(context.WithoutCancel(ctx), task.ID, pid, check.ExitCode)
	saveErr := s.db.SaveCheck(context.WithoutCancel(ctx), check)
	if endErr != nil {
		return check, endErr
	}
	if saveErr != nil {
		return check, saveErr
	}
	if check.Status == "cancelled" {
		return check, &checkRunError{kind: "cancelled", err: ctx.Err()}
	}
	if terminationErr != nil {
		return check, &checkRunError{kind: "inconclusive", err: terminationErr}
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

func waitForProcess(ctx context.Context, command *exec.Cmd) (error, bool, error) {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err, false, nil
	case <-ctx.Done():
		terminationErr := terminateProcessGroup(command.Process.Pid)
		select {
		case err := <-done:
			if terminationErr == nil {
				terminationErr = waitForProcessGroupGone(command.Process.Pid)
			}
			return err, true, terminationErr
		case <-time.After(250 * time.Millisecond):
			if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && terminationErr == nil {
				terminationErr = fmt.Errorf("%w: force terminate process group %d: %v", harness.ErrProcessTerminationUnconfirmed, command.Process.Pid, err)
			}
			err := <-done
			if terminationErr == nil {
				terminationErr = waitForProcessGroupGone(command.Process.Pid)
			}
			return err, true, terminationErr
		}
	}
}

func terminateProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("%w: terminate process group %d: %v", harness.ErrProcessTerminationUnconfirmed, pid, err)
	}
	return nil
}

func waitForProcessGroupGone(pid int) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("%w: inspect process group %d: %v", harness.ErrProcessTerminationUnconfirmed, pid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: process group %d", harness.ErrProcessTerminationUnconfirmed, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func (s *Service) builderValidator(ctx context.Context, task store.Task, profiles map[string]Materialization) validator {
	return s.quality.builderValidator(ctx, task, profiles)
}

func (s *Service) persistBuilderEvidence(ctx context.Context, task store.Task, phase store.Phase, payload string) error {
	return s.quality.persistBuilderEvidence(ctx, task, phase, payload)
}

func (s *Service) runChecks(ctx context.Context, task store.Task, phase store.Phase, repository store.TaskRepository, checks []Check, checkPhase, baseline string) error {
	return s.quality.runChecks(ctx, task, phase, repository, checks, checkPhase, baseline)
}

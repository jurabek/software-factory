// Package checkrun executes one deterministic repository check as a process
// group: bounded output capture, cancellation that terminates the whole group,
// and classification into passed, failed, inconclusive, or cancelled. It owns
// no persistence; callers record the returned Result.
package checkrun

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxCapturedOutput bounds the retained tail of a check's combined output.
const MaxCapturedOutput = 64 << 10

const (
	Passed       = "passed"
	Failed       = "failed"
	Inconclusive = "inconclusive"
	Cancelled    = "cancelled"
)

// Check is one shell command run in a working directory.
type Check struct {
	Command string
	Dir     string
}

// Result is the classified outcome of one check run.
type Result struct {
	Status    string
	PID       int
	ExitCode  int
	Output    string
	StartErr  error
	StartedAt time.Time
	EndedAt   time.Time
}

// Run executes a check and classifies the result. onStart, when set, observes
// the started process; if it fails the process group is terminated, reaped,
// and its error returned with Result.PID set. Check failures are reported in
// Result, never as an error.
func Run(ctx context.Context, check Check, onStart func(pid int) error) (Result, error) {
	result := Result{ExitCode: -1, StartedAt: time.Now().UTC()}
	capture := &tailCapture{limit: MaxCapturedOutput}
	command := exec.Command("/bin/sh", "-c", check.Command)
	command.Dir = check.Dir
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = capture
	command.Stderr = capture
	if err := command.Start(); err != nil {
		result.Status = Inconclusive
		result.Output = err.Error()
		result.StartErr = err
		result.EndedAt = time.Now().UTC()
		return result, nil
	}
	result.PID = command.Process.Pid
	if onStart != nil {
		if err := onStart(result.PID); err != nil {
			terminateProcessGroup(result.PID)
			_ = command.Wait()
			return result, err
		}
	}
	waitErr, cancelled := waitForProcess(ctx, command)
	result.ExitCode = processExitCode(waitErr)
	result.Output = capture.String()
	switch {
	case cancelled || errors.Is(ctx.Err(), context.Canceled):
		result.Status = Cancelled
	case waitErr != nil && setupFailure(result.ExitCode, result.Output):
		result.Status = Inconclusive
	case waitErr != nil:
		result.Status = Failed
	default:
		result.Status = Passed
	}
	result.EndedAt = time.Now().UTC()
	return result, nil
}

func setupFailure(exitCode int, output string) bool {
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
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	return -1
}

type tailCapture struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (capture *tailCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	length := len(data)
	capture.data = append(capture.data, data...)
	if len(capture.data) > capture.limit {
		capture.data = capture.data[len(capture.data)-capture.limit:]
	}
	return length, nil
}

func (capture *tailCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return string(capture.data)
}

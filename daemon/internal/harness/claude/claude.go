package claude

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

const (
	maxStderrBytes = 64 << 10
	gracePeriod    = 2 * time.Second
)

// Supported aliases and effort subset for v1.
var (
	supportedAliases = map[string]bool{"sonnet": true, "opus": true}
	supportedEffort  = map[string]bool{"low": true, "medium": true, "high": true}
)

// Config is the operator-configured Claude policy.
type Config struct {
	// Path to the claude executable (CLAUDE_PATH, default claude).
	Path string
	// AllowedTools are additive to inherited Claude permissions, not an
	// exclusive allowlist. Empty by default.
	AllowedTools []string
	// Models are explicitly configured full model IDs (anthropic/<id> or bare).
	Models []string
	// ConfigRoot overrides the native Claude state root for tests.
	ConfigRoot string
}

// Harness implements harness.Harness for Claude Code print-mode streaming.
type Harness struct {
	Config Config
}

func (h Harness) executable() string {
	if h.Config.Path != "" {
		return h.Config.Path
	}
	return "claude"
}

// Models returns the documented small catalog: sonnet and opus aliases plus
// explicitly configured full model IDs. Provider is always anthropic.
func (h Harness) Models(context.Context) ([]harness.Model, error) {
	if _, err := exec.LookPath(h.executable()); err != nil {
		return nil, fmt.Errorf("claude executable not found: %w", err)
	}
	seen := map[string]bool{}
	var out []harness.Model
	for _, alias := range []string{"sonnet", "opus"} {
		seen["anthropic/"+alias] = true
		out = append(out, harness.Model{Provider: "anthropic", ID: alias, Thinking: []string{"low", "medium", "high"}})
	}
	for _, configured := range h.Config.Models {
		id := strings.TrimPrefix(configured, "anthropic/")
		if strings.Contains(id, "/") {
			// Pi/third-party provider prefixes are rejected explicitly.
			continue
		}
		key := "anthropic/" + id
		if seen[key] || id == "" {
			continue
		}
		seen[key] = true
		out = append(out, harness.Model{Provider: "anthropic", ID: id, Thinking: []string{"low", "medium", "high"}})
	}
	return out, nil
}

// nativeModel strips only the exact anthropic/ prefix, accepts bare supported
// aliases and configured IDs, and rejects third-party provider prefixes.
func (h Harness) nativeModel(model string) (string, error) {
	if strings.Contains(model, "/") {
		provider, id, _ := strings.Cut(model, "/")
		if provider != "anthropic" {
			return "", fmt.Errorf("model %q uses unsupported provider %q", model, provider)
		}
		if supportedAliases[id] {
			return id, nil
		}
		for _, configured := range h.Config.Models {
			if strings.TrimPrefix(configured, "anthropic/") == id {
				return id, nil
			}
		}
		return "", fmt.Errorf("model %q is not configured", model)
	}
	if supportedAliases[model] {
		return model, nil
	}
	for _, configured := range h.Config.Models {
		if strings.TrimPrefix(configured, "anthropic/") == model {
			return model, nil
		}
	}
	return "", fmt.Errorf("model %q is not supported (want sonnet, opus, or configured claude.models)", model)
}

func validateEffort(thinking string) (string, error) {
	if supportedEffort[thinking] {
		return thinking, nil
	}
	return "", fmt.Errorf("thinking %q unsupported for claude (want low, medium, or high)", thinking)
}

// BuildArgs constructs the argv directly, never through a shell. It is
// exported for fake-executable integration tests.
func (h Harness) BuildArgs(request harness.Request, resume bool) ([]string, error) {
	nativeModel, err := h.nativeModel(request.Model)
	if err != nil {
		return nil, err
	}
	effort, err := validateEffort(request.Thinking)
	if err != nil {
		return nil, err
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if resume {
		args = append(args, "--resume", request.SessionID)
	} else {
		args = append(args, "--session-id", request.SessionID)
	}
	args = append(args, "--model", nativeModel)
	args = append(args, "--append-system-prompt", request.SystemPrompt)
	args = append(args, "--permission-mode", "dontAsk", "--permission-prompts", "none")
	if len(h.Config.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(h.Config.AllowedTools, ","))
	}
	args = append(args, "--effort", effort)
	for _, dir := range request.AdditionalDirectories {
		if dir != "" {
			args = append(args, "--add-dir", dir)
		}
	}
	return args, nil
}

// Run executes one single-input turn. Live stdout is the sole source of
// normalized events; the native transcript is archived after the run.
func (h Harness) Run(parent context.Context, request harness.Request, sink harness.EventSink) (harness.Result, error) {
	if _, err := uuid.Parse(request.SessionID); err != nil {
		return harness.Result{}, fmt.Errorf("claude session id must be a UUID: %w", err)
	}
	if err := os.MkdirAll(request.SessionDirectory, 0o700); err != nil {
		return harness.Result{}, fmt.Errorf("create claude session directory: %w", err)
	}
	root, err := configRoot(h.Config.ConfigRoot)
	if err != nil {
		return harness.Result{}, err
	}
	nativePath, nativeOK, err := findNativeTranscript(root, request.SessionID)
	if err != nil {
		return harness.Result{SessionID: request.SessionID}, err
	}
	resume := request.Resume
	if !request.Resume {
		// Reserved but uninitialized: matching usable state means resume,
		// proven absence means create. Ambiguity already errors above.
		resume = nativeOK
	} else if !nativeOK {
		// Previously ready with missing native state: explicit error, never
		// an automatic fresh start.
		return harness.Result{SessionID: request.SessionID, SessionReady: true, AccountingComplete: false}, fmt.Errorf("claude native session %s is missing", request.SessionID)
	}
	args, err := h.BuildArgs(request, resume)
	if err != nil {
		return harness.Result{SessionID: request.SessionID}, err
	}

	ctx := parent
	cancel := context.CancelFunc(func() {})
	if request.DeadlineMS > 0 {
		ctx, cancel = context.WithTimeout(parent, time.Duration(request.DeadlineMS)*time.Millisecond)
	}
	defer cancel()

	cmd := exec.Command(h.executable(), args...)
	cmd.Dir = request.CWD
	cmd.Stdin = strings.NewReader(request.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return harness.Result{SessionID: request.SessionID}, fmt.Errorf("claude stdout: %w", err)
	}
	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderr
	rawPath := filepath.Join(request.SessionDirectory, "raw-output.jsonl")
	raw, err := os.OpenFile(rawPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return harness.Result{SessionID: request.SessionID}, fmt.Errorf("open claude raw output: %w", err)
	}
	defer raw.Close()
	// Invocation boundary marker separates correction/resume runs.
	_, _ = fmt.Fprintf(raw, "{\"type\":\"claude.invocation_boundary\",\"session_id\":%q,\"resume\":%v}\n", request.SessionID, resume)

	if err := cmd.Start(); err != nil {
		return harness.Result{SessionID: request.SessionID}, fmt.Errorf("start claude: %w", err)
	}
	started := time.Now()
	result := harness.Result{SessionID: request.SessionID, Provider: "anthropic", SessionReady: true, AccountingComplete: true}
	emit := func(event harness.Event) error {
		if sink == nil {
			return nil
		}
		return sink(parent, event)
	}
	if err := emit(session.NewProcessStart(session.ProcessStartPayload{PID: cmd.Process.Pid, Command: displayArgs(h.executable(), args)})); err != nil {
		terminate(cmd.Process.Pid)
		_ = cmd.Wait()
		return result, fmt.Errorf("emit claude process start: %w", err)
	}
	terminated := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminate(cmd.Process.Pid)
		case <-terminated:
		}
	}()

	decoded, sinkErr, decodeErr := teeDecode(ctx, stdout, raw, emit)
	decoded.SessionID = request.SessionID
	if decoded.Provider == "" {
		decoded.Provider = "anthropic"
	}
	if decoded.Model != "" {
		result.Model = decoded.Model
	}
	result.Text = decoded.Text
	result.Usage = decoded.Usage
	result.ContextTokens = decoded.ContextTokens
	result.ContextWindow = decoded.ContextWindow
	if decoded.SessionID != "" {
		result.SessionID = decoded.SessionID
	}
	result.SessionReady = result.SessionReady || decoded.SessionReady
	result.AccountingComplete = decoded.AccountingComplete
	if sinkErr != nil {
		terminate(cmd.Process.Pid)
		drain(stdout)
		_ = cmd.Wait()
		close(terminated)
		endProcess(parent, emit, cmd.Process.Pid, -1, time.Since(started))
		return result, fmt.Errorf("emit claude event: %w", sinkErr)
	}
	if decodeErr != nil {
		terminate(cmd.Process.Pid)
		drain(stdout)
		_ = cmd.Wait()
		close(terminated)
		endProcess(parent, emit, cmd.Process.Pid, -1, time.Since(started))
		result.AccountingComplete = false
		return result, fmt.Errorf("read claude output: %w", decodeErr)
	}
	waitErr := cmd.Wait()
	close(terminated)
	result.ExitCode = exitCode(waitErr)
	endProcess(parent, emit, cmd.Process.Pid, result.ExitCode, time.Since(started))
	if ctx.Err() != nil {
		result.AccountingComplete = false
		return result, fmt.Errorf("claude interrupted: %w", ctx.Err())
	}
	if decodeErr == nil && result.Text == "" && waitErr != nil {
		result.AccountingComplete = false
		return result, fmt.Errorf("claude exited %d: %s", result.ExitCode, stderr.String())
	}
	if waitErr != nil && result.Text == "" {
		result.AccountingComplete = false
		return result, fmt.Errorf("claude exited %d: %s", result.ExitCode, stderr.String())
	}
	// Archive only this task's identified session after the run.
	if archived, archiveErr := archiveNative(nativePathForArchive(root, request.SessionID, nativePath), request.SessionDirectory, request.SessionID); archiveErr != nil {
		result.AccountingComplete = false
		if waitErr != nil {
			return result, fmt.Errorf("claude archive: %v; process: %w", archiveErr, waitErr)
		}
		return result, fmt.Errorf("claude archive: %w", archiveErr)
	} else if archived != "" {
		result.NativeTranscriptPath = archived
	}
	if waitErr != nil {
		return result, fmt.Errorf("claude exited %d", result.ExitCode)
	}
	return result, nil
}

func nativePathForArchive(root, uuid, found string) string {
	if found != "" {
		return found
	}
	// Re-check after the run: the CLI may have created the transcript.
	path, ok, _ := findNativeTranscript(root, uuid)
	if !ok {
		return ""
	}
	return path
}

func endProcess(ctx context.Context, emit func(harness.Event) error, pid, exit int, elapsed time.Duration) {
	_ = emit(session.NewProcessEnd(session.ProcessEndPayload{PID: pid, ExitCode: exit, DurationMS: elapsed.Milliseconds()}))
}

// teeDecode preserves raw bytes before decoding, bounds records, and reports
// sink vs decode failures separately so Run can terminate/drain the child
// before Wait and avoid a blocked-pipe deadlock.
func teeDecode(ctx context.Context, stdout io.Reader, raw *os.File, emit func(harness.Event) error) (StreamSummary, error, error) {
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	decodeDone := make(chan struct{})
	var summary StreamSummary
	var decodeErr error
	go func() {
		defer close(decodeDone)
		summary, decodeErr = DecodeStream(ctx, pipeReader, func(_ context.Context, event harness.Event) error {
			return emit(event)
		})
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	var sinkErr error
	for scanner.Scan() {
		line := scanner.Text()
		if raw != nil {
			if _, err := raw.WriteString(line + "\n"); err != nil {
				sinkErr = fmt.Errorf("write claude raw output: %w", err)
				break
			}
		}
		if _, err := pipeWriter.Write([]byte(line + "\n")); err != nil {
			break
		}
	}
	if scanErr := scanner.Err(); scanErr != nil && sinkErr == nil {
		sinkErr = scanErr
	}
	pipeWriter.Close()
	<-decodeDone
	if sinkErr != nil {
		return summary, sinkErr, nil
	}
	return summary, nil, decodeErr
}

func drain(r io.Reader) {
	buffer := make([]byte, 32*1024)
	for {
		if _, err := r.Read(buffer); err != nil {
			return
		}
	}
}

func terminate(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(gracePeriod / 4)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}

func displayArgs(path string, args []string) string {
	// Never include secrets: argv carries no credentials by construction.
	parts := append([]string{path}, args...)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, " \t\n") {
			quoted = append(quoted, fmt.Sprintf("%q", part))
			continue
		}
		quoted = append(quoted, part)
	}
	return strings.Join(quoted, " ")
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.data = append(b.data, data...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(data), nil
}

func (b *boundedBuffer) String() string { return strings.TrimSpace(string(b.data)) }

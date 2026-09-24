package pi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

const (
	rpcIdleTimeout   = 90 * time.Second
	rpcAbortGrace    = 15 * time.Second
	rpcLineBuffer    = 1024
	rpcCommandPrefix = "factory"
)

// Runner is a session-oriented Pi harness. It embeds Harness so it also serves
// as the disk-backed NativeReader for restart reconciliation and timeline
// hydration. Open reuses one live Pi process per native session across turns
// and stage executions until the process goes idle.
type Runner struct {
	Harness
	pool *sessionPool
}

// New constructs a session-oriented Pi runner that owns a persistent process
// pool.
func New(path, extensionPath string) *Runner {
	return &Runner{Path: path, ExtensionPath: extensionPath, pool: newSessionPool()}
}

// Open returns a handle to the native session, starting or reusing one Pi
// process per session and CWD.
func (r *Runner) Open(ctx context.Context, spec harness.SessionSpec) (harness.Session, error) {
	return r.pool.open(ctx, r, spec)
}

// Shutdown terminates every pooled process.
func (r *Runner) Shutdown() { r.pool.shutdown() }

type sessionPool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
}

type poolEntry struct {
	session *rpcSession
	refs    int
	timer   *time.Timer
}

func newSessionPool() *sessionPool {
	return &sessionPool{entries: map[string]*poolEntry{}}
}

func poolKey(spec harness.SessionSpec) string {
	return spec.SessionDirectory + "\x00" + spec.SessionID
}

func (p *sessionPool) open(ctx context.Context, runner *Runner, spec harness.SessionSpec) (harness.Session, error) {
	key := poolKey(spec)
	for {
		p.mu.Lock()
		entry := p.entries[key]
		if entry != nil && !entry.session.exited.Load() && entry.session.matches(spec) {
			if entry.timer != nil {
				entry.timer.Stop()
				entry.timer = nil
			}
			entry.refs++
			p.mu.Unlock()
			return &pooledSession{pool: p, key: key, session: entry.session}, nil
		}
		if entry != nil {
			delete(p.entries, key)
			if entry.timer != nil {
				entry.timer.Stop()
			}
			p.mu.Unlock()
			entry.session.close()
			continue
		}
		p.mu.Unlock()
		session, err := startRPCSession(ctx, runner, spec)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		if existing := p.entries[key]; existing != nil {
			p.mu.Unlock()
			session.close()
			continue
		}
		p.entries[key] = &poolEntry{session: session, refs: 1}
		p.mu.Unlock()
		return &pooledSession{pool: p, key: key, session: session}, nil
	}
}

func (p *sessionPool) release(key string, session *rpcSession) {
	p.mu.Lock()
	entry := p.entries[key]
	if entry == nil || entry.session != session {
		p.mu.Unlock()
		return
	}
	entry.refs--
	if entry.refs > 0 {
		p.mu.Unlock()
		return
	}
	entry.timer = time.AfterFunc(rpcIdleTimeout, func() {
		p.mu.Lock()
		current := p.entries[key]
		if current == entry && current.refs == 0 {
			delete(p.entries, key)
			p.mu.Unlock()
			entry.session.close()
			return
		}
		p.mu.Unlock()
	})
	p.mu.Unlock()
}

func (p *sessionPool) shutdown() {
	p.mu.Lock()
	entries := p.entries
	p.entries = map[string]*poolEntry{}
	p.mu.Unlock()
	for _, entry := range entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		entry.session.close()
	}
}

// pooledSession is one reference to a pooled Pi process.
type pooledSession struct {
	pool    *sessionPool
	key     string
	session *rpcSession
	once    sync.Once
}

func (s *pooledSession) Prompt(ctx context.Context, prompt harness.Prompt, sink harness.EventSink) (harness.Result, error) {
	return s.session.Prompt(ctx, prompt, sink)
}

func (s *pooledSession) Stats(ctx context.Context) (harness.Stats, error) {
	return s.session.Stats(ctx)
}

func (s *pooledSession) Entries(ctx context.Context) ([]harness.NativeEntry, error) {
	return s.session.Entries(ctx)
}

func (s *pooledSession) Report(ctx context.Context, requestID string) (harness.Report, bool, error) {
	return s.session.Report(ctx, requestID)
}

func (s *pooledSession) Close() error {
	s.once.Do(func() { s.pool.release(s.key, s.session) })
	return nil
}

// rpcSession is a live Pi RPC process bound to one native session.
type rpcSession struct {
	runner *Runner
	spec   harness.SessionSpec

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *tailWriter
	display string
	writeMu sync.Mutex
	waitOne sync.Once
	waitErr error

	interact sync.Mutex
	closed   bool
	exited   atomic.Bool

	lines chan []byte

	commandSeq int
	snapshot   *sessionSnapshot
}

type sessionSnapshot struct {
	records []sessionRecord
	leaf    string
}

func startRPCSession(ctx context.Context, runner *Runner, spec harness.SessionSpec) (*rpcSession, error) {
	if spec.SessionDirectory != "" {
		if err := os.MkdirAll(spec.SessionDirectory, 0o700); err != nil {
			return nil, fmt.Errorf("create pi session directory: %w", err)
		}
	}
	provider, model := splitModel(spec.Model)
	args := []string{"--mode", "rpc", "--provider", provider, "--model", model, "--thinking", spec.Thinking, "--session-id", spec.SessionID, "--session-dir", spec.SessionDirectory, "--system-prompt", spec.SystemPrompt, "--approve"}
	if runner.ExtensionPath != "" {
		args = append(args, "-e", runner.ExtensionPath)
	}
	cmd := exec.Command(runner.Path, args...)
	cmd.Dir = spec.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stdout: %w", err)
	}
	stderr := &tailWriter{limit: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pi rpc: %w", err)
	}
	session := &rpcSession{runner: runner, spec: spec, cmd: cmd, stdin: stdin, stderr: stderr, display: displayCommand(runner.Path, args), lines: make(chan []byte, rpcLineBuffer)}
	go session.readLoop(stdout)
	return session, nil
}

func (s *rpcSession) readLoop(stdout io.ReadCloser) {
	defer close(s.lines)
	defer s.exited.Store(true)
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\r\n"); trimmed != "" {
			select {
			case s.lines <- []byte(trimmed):
			case <-time.After(5 * time.Second):
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *rpcSession) matches(spec harness.SessionSpec) bool {
	return s.spec.SessionID == spec.SessionID &&
		s.spec.SessionDirectory == spec.SessionDirectory &&
		s.spec.CWD == spec.CWD &&
		s.spec.Model == spec.Model &&
		s.spec.Thinking == spec.Thinking &&
		s.spec.SystemPrompt == spec.SystemPrompt
}

func (s *rpcSession) close() {
	s.interact.Lock()
	defer s.interact.Unlock()
	s.closeLocked()
}

func (s *rpcSession) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.writeMu.Lock()
	_ = s.stdin.Close()
	s.writeMu.Unlock()
	if s.cmd.Process != nil {
		terminateGroup(s.cmd.Process.Pid)
	}
	_ = s.waitProcess()
}

// waitProcess waits for the child exactly once and caches its error.
func (s *rpcSession) waitProcess() error {
	s.waitOne.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

func (s *rpcSession) processPID() int {
	if s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *rpcSession) nextCommandID() string {
	s.commandSeq++
	return fmt.Sprintf("%s-%d", rpcCommandPrefix, s.commandSeq)
}

func (s *rpcSession) send(payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return errors.New("pi rpc session is closed")
	}
	if _, err = s.stdin.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write pi rpc command: %w", err)
	}
	return nil
}

// Prompt sends one user message and streams events until the run settles or the
// context is cancelled.
func (s *rpcSession) Prompt(ctx context.Context, prompt harness.Prompt, sink harness.EventSink) (harness.Result, error) {
	s.interact.Lock()
	defer s.interact.Unlock()
	provider, model := splitModel(s.spec.Model)
	result := harness.Result{SessionID: s.spec.SessionID, Provider: provider, Model: model, SessionReady: true}
	if s.closed {
		return result, errors.New("pi rpc session is closed")
	}
	s.snapshot = nil
	runCtx := ctx
	if prompt.DeadlineMS > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(prompt.DeadlineMS)*time.Millisecond)
		defer cancel()
	}
	message, err := s.promptMessage(prompt)
	if err != nil {
		return result, err
	}
	state := &promptState{tools: map[string]toolStart{}, sink: withRequest(sink, prompt.RequestID)}
	started := time.Now()
	if err = emit(runCtx, state.sink, session.NewProcessStart(session.ProcessStartPayload{PID: s.processPID(), Command: s.display})); err != nil {
		return result, fmt.Errorf("emit pi process start: %w", err)
	}
	defer func() {
		_ = emit(ctx, state.sink, session.NewProcessEnd(session.ProcessEndPayload{PID: s.processPID(), ExitCode: result.ExitCode, DurationMS: time.Since(started).Milliseconds()}))
	}()
	if err = s.send(map[string]any{"id": s.nextCommandID(), "type": "prompt", "message": message}); err != nil {
		return result, err
	}
	settled := false
	for !settled {
		select {
		case <-runCtx.Done():
			_ = s.send(map[string]any{"type": "abort"})
			s.drainUntilSettled(state, rpcAbortGrace)
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return result, fmt.Errorf("pi turn exceeded its deadline: %w", runCtx.Err())
			}
			return result, runCtx.Err()
		case line, ok := <-s.lines:
			if !ok {
				result.ExitCode = exitCode(s.waitProcess())
				if strings.TrimSpace(state.result.Text) == "" {
					return result, fmt.Errorf("pi rpc process exited: %s", s.stderr.String())
				}
				return result, errors.New("pi rpc process exited before the run settled")
			}
			var event map[string]any
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			settled, err = s.handleEvent(runCtx, event, state)
			if err != nil {
				return result, err
			}
		}
	}
	result = state.result
	result.SessionID = s.spec.SessionID
	result.Provider = provider
	result.Model = model
	result.SessionReady = true
	return result, nil
}

type promptState struct {
	result harness.Result
	tools  map[string]toolStart
	sink   harness.EventSink
}

func (s *rpcSession) handleEvent(ctx context.Context, event map[string]any, state *promptState) (bool, error) {
	switch stringValue(event, "type") {
	case "response":
		if success, ok := event["success"].(bool); ok && !success {
			return true, fmt.Errorf("pi prompt rejected: %v", event["error"])
		}
		return false, nil
	case "extension_ui_request":
		s.handleUIRequest(event)
		return false, nil
	case "entry_appended":
		return false, nil
	case "agent_settled":
		return true, nil
	default:
		if err := processEvent(event, state.tools, &state.result, state.sink, ctx); err != nil {
			return false, err
		}
		return false, nil
	}
}

// drainUntilSettled consumes the remaining events after an abort so the
// process returns to an idle, reusable state.
func (s *rpcSession) drainUntilSettled(state *promptState, grace time.Duration) {
	drain := &promptState{tools: state.tools}
	deadline := time.After(grace)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				return
			}
			var event map[string]any
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if stringValue(event, "type") == "agent_settled" {
				return
			}
			_, _ = s.handleEvent(context.Background(), event, drain)
		case <-deadline:
			return
		}
	}
}

// handleUIRequest answers extension dialogs with a cancellation and ignores
// fire-and-forget notifications, so a headless turn never blocks on UI.
func (s *rpcSession) handleUIRequest(event map[string]any) {
	id := stringValue(event, "id")
	if id == "" {
		return
	}
	switch stringValue(event, "method") {
	case "select", "input", "editor":
		_ = s.send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	case "confirm":
		_ = s.send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	}
}

func (s *rpcSession) promptMessage(prompt harness.Prompt) (string, error) {
	if s.runner.ExtensionPath == "" || prompt.RequestID == "" {
		return prompt.Text, nil
	}
	payload, err := json.Marshal(map[string]any{"requestId": prompt.RequestID, "attempt": prompt.Attempt, "prompt": prompt.Text})
	if err != nil {
		return "", fmt.Errorf("encode factory request: %w", err)
	}
	return "/factory-run " + base64.RawURLEncoding.EncodeToString(payload), nil
}

// Stats returns native session totals from get_session_stats.
func (s *rpcSession) Stats(ctx context.Context) (harness.Stats, error) {
	s.interact.Lock()
	defer s.interact.Unlock()
	if s.closed {
		return harness.Stats{}, errors.New("pi rpc session is closed")
	}
	data, err := s.command(ctx, "get_session_stats")
	if err != nil {
		return harness.Stats{}, err
	}
	stats := statsFromRPC(data)
	snapshot, snapErr := s.ensureSnapshot(ctx)
	if snapErr == nil {
		stats.LeafID = snapshot.leaf
	}
	return stats, nil
}

// Entries returns every native session entry, including abandoned branches and
// pre-compaction history.
func (s *rpcSession) Entries(ctx context.Context) ([]harness.NativeEntry, error) {
	s.interact.Lock()
	defer s.interact.Unlock()
	if s.closed {
		return nil, errors.New("pi rpc session is closed")
	}
	snapshot, err := s.ensureSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return nativeEntries(snapshot.records), nil
}

// Report resolves the assistant text authored for a factory request from its
// native entry subtree.
func (s *rpcSession) Report(ctx context.Context, requestID string) (harness.Report, bool, error) {
	s.interact.Lock()
	defer s.interact.Unlock()
	if s.closed {
		return harness.Report{}, false, errors.New("pi rpc session is closed")
	}
	if requestID == "" {
		return harness.Report{}, false, nil
	}
	snapshot, err := s.ensureSnapshot(ctx)
	if err != nil {
		return harness.Report{}, false, err
	}
	report, ok := reportFromRecords(snapshot.records, requestID)
	return report, ok, nil
}

// ensureSnapshot fetches entries once per settled turn and caches them for the
// report/leaf/entries reads that follow.
func (s *rpcSession) ensureSnapshot(ctx context.Context) (*sessionSnapshot, error) {
	if s.snapshot != nil {
		return s.snapshot, nil
	}
	data, err := s.command(ctx, "get_entries")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Entries []sessionRecord `json:"entries"`
		LeafID  *string         `json:"leafId"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode pi entries: %w", err)
	}
	leaf := ""
	if payload.LeafID != nil {
		leaf = *payload.LeafID
	}
	s.snapshot = &sessionSnapshot{records: payload.Entries, leaf: leaf}
	return s.snapshot, nil
}

// command sends an RPC command and waits for its correlated response, skipping
// unrelated events and answering UI requests.
func (s *rpcSession) command(ctx context.Context, command string) (json.RawMessage, error) {
	id := s.nextCommandID()
	if err := s.send(map[string]any{"id": id, "type": command}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-s.lines:
			if !ok {
				return nil, errors.New("pi rpc process exited")
			}
			var event map[string]any
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			switch stringValue(event, "type") {
			case "response":
				if stringValue(event, "id") != id {
					if success, ok := event["success"].(bool); ok && !success {
						return nil, fmt.Errorf("pi %s failed: %v", command, event["error"])
					}
					continue
				}
				if success, ok := event["success"].(bool); ok && !success {
					return nil, fmt.Errorf("pi %s failed: %v", command, event["error"])
				}
				data, err := json.Marshal(event["data"])
				if err != nil {
					return nil, err
				}
				return data, nil
			case "extension_ui_request":
				s.handleUIRequest(event)
			}
		}
	}
}

func (s *rpcSession) String() string {
	return filepath.Join(s.spec.SessionDirectory, s.spec.SessionID)
}

// statsFromRPC maps a get_session_stats payload onto harness totals.
func statsFromRPC(data json.RawMessage) harness.Stats {
	var payload struct {
		Tokens struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
			Total      int `json:"total"`
		} `json:"tokens"`
		Cost         float64 `json:"cost"`
		ContextUsage *struct {
			Tokens        int `json:"tokens"`
			ContextWindow int `json:"contextWindow"`
		} `json:"contextUsage"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return harness.Stats{}
	}
	stats := harness.Stats{Usage: harness.Usage{Input: payload.Tokens.Input, Output: payload.Tokens.Output, CacheRead: payload.Tokens.CacheRead, CacheWrite: payload.Tokens.CacheWrite, TotalTokens: payload.Tokens.Total, Cost: payload.Cost}}
	if payload.ContextUsage != nil {
		stats.ContextTokens = payload.ContextUsage.Tokens
		stats.ContextWindow = payload.ContextUsage.ContextWindow
	}
	return stats
}

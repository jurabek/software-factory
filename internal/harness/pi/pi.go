package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jurabek/software-factory/internal/harness"
)

const (
	maxJSONLLine = 4 << 20
	maxStderr    = 64 << 10
	maxEventText = 16 << 10
)

type Harness struct{ Path string }

func (h Harness) Models(ctx context.Context) ([]harness.Model, error) {
	output, err := exec.CommandContext(ctx, h.Path, "--list-models").Output()
	if err != nil {
		return nil, fmt.Errorf("list pi models: %w", err)
	}
	var models []harness.Model
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.Contains(fields[0], "/") {
			continue
		}
		parts := strings.SplitN(fields[0], "/", 2)
		window := 0
		for _, field := range fields[1:] {
			if n, parseErr := strconv.Atoi(strings.ReplaceAll(field, ",", "")); parseErr == nil {
				window = n
			}
		}
		models = append(models, harness.Model{Provider: parts[0], ID: parts[1], ContextWindow: window})
	}
	return models, nil
}

func (h Harness) Run(parent context.Context, request harness.Request, sink harness.EventSink) (harness.Result, error) {
	ctx := parent
	if request.DeadlineMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, time.Duration(request.DeadlineMS)*time.Millisecond)
		defer cancel()
	}
	provider, model := splitModel(request.Model)
	args := []string{"-p", "--mode", "json", "--provider", provider, "--model", model, "--thinking", request.Thinking, "--session-id", request.SessionID, "--session-dir", request.SessionDirectory, "--system-prompt", request.SystemPrompt, "--approve"}
	if len(request.Tools) > 0 {
		args = append(args, "--tools", strings.Join(request.Tools, ","))
	}
	args = append(args, request.Prompt)

	cmd := exec.Command(h.Path, args...)
	cmd.Dir = request.CWD
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return harness.Result{}, fmt.Errorf("pi stdout: %w", err)
	}
	stderr := &tailWriter{limit: maxStderr}
	cmd.Stderr = stderr
	if err := ensureOutputPaths(request); err != nil {
		return harness.Result{}, err
	}
	raw, err := openRaw(request.RawOutputPath)
	if err != nil {
		return harness.Result{}, err
	}
	if raw != nil {
		defer raw.Close()
	}
	if err := cmd.Start(); err != nil {
		return harness.Result{}, fmt.Errorf("start pi: %w", err)
	}
	started := time.Now()
	emit(parent, sink, harness.Event{Type: "process_start", Name: "pi", Payload: map[string]any{"pid": cmd.Process.Pid, "command": displayCommand(h.Path, args), "started_at": started.UTC()}})

	terminated := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateGroup(cmd.Process.Pid)
		case <-terminated:
		}
	}()

	result, scanErr := consume(stdout, raw, sink, parent)
	waitErr := cmd.Wait()
	close(terminated)
	result.SessionID = request.SessionID
	result.Provider = provider
	result.Model = model
	result.ExitCode = exitCode(waitErr)
	emit(parent, sink, harness.Event{Type: "process_end", Name: "pi", Payload: map[string]any{"pid": cmd.Process.Pid, "exit_code": result.ExitCode, "duration_ms": time.Since(started).Milliseconds(), "ended_at": time.Now().UTC()}})
	if scanErr != nil {
		return result, fmt.Errorf("read pi output: %w", scanErr)
	}
	if ctx.Err() != nil {
		return result, fmt.Errorf("pi interrupted: %w", ctx.Err())
	}
	if waitErr != nil && strings.TrimSpace(result.Text) == "" {
		return result, fmt.Errorf("pi exited %d: %s", result.ExitCode, stderr.String())
	}
	return result, nil
}

func ensureOutputPaths(request harness.Request) error {
	for _, path := range []string{request.SessionDirectory, filepath.Dir(request.RawOutputPath)} {
		if path == "" || path == "." {
			continue
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("create pi output directory: %w", err)
		}
	}
	return nil
}

func consume(stdout io.Reader, raw *os.File, sink harness.EventSink, ctx context.Context) (harness.Result, error) {
	var result harness.Result
	toolStarts := map[string]toolStart{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLine)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if raw != nil {
			if _, err := raw.WriteString(line + "\n"); err != nil {
				return result, fmt.Errorf("write raw output: %w", err)
			}
			if err := raw.Sync(); err != nil {
				return result, fmt.Errorf("flush raw output: %w", err)
			}
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		processEvent(event, toolStarts, &result, sink, ctx)
	}
	return result, scanner.Err()
}

type toolStart struct {
	name      string
	arguments any
	startedAt time.Time
}

func processEvent(event map[string]any, tools map[string]toolStart, result *harness.Result, sink harness.EventSink, ctx context.Context) {
	typeName, _ := event["type"].(string)
	if typeName == "message_end" {
		if text := assistantText(event); strings.TrimSpace(text) != "" {
			result.Text = text
		}
		accumulateUsage(event, result)
	}
	id := stringValue(event, "toolCallId", "tool_call_id")
	switch typeName {
	case "tool_execution_start":
		tools[id] = toolStart{name: stringValue(event, "toolName", "tool_name", "name"), arguments: firstValue(event, "args", "arguments"), startedAt: eventTime(event, time.Now())}
	case "tool_execution_end":
		start := tools[id]
		ended := eventTime(event, time.Now())
		payload := map[string]any{"tool_call_id": id, "tool": start.name, "arguments": boundedJSON(start.arguments), "label": toolLabel(start.arguments), "started_at": start.startedAt.UTC(), "ended_at": ended.UTC(), "duration_ms": ended.Sub(start.startedAt).Milliseconds(), "success": !boolValue(event, "isError", "error"), "result": truncate(fmt.Sprint(firstValue(event, "result", "output")), maxEventText)}
		emit(ctx, sink, harness.Event{Type: "tool_call", Name: start.name, Payload: payload})
		delete(tools, id)
	default:
		emit(ctx, sink, harness.Event{Type: typeName, Payload: event})
	}
}

func accumulateUsage(event map[string]any, result *harness.Result) {
	message, _ := event["message"].(map[string]any)
	usage, _ := firstValue(message, "usage").(map[string]any)
	if usage == nil {
		usage, _ = event["usage"].(map[string]any)
	}
	result.Usage.Input += intValue(usage, "input")
	result.Usage.Output += intValue(usage, "output")
	result.Usage.CacheRead += intValue(usage, "cacheRead", "cache_read")
	result.Usage.CacheWrite += intValue(usage, "cacheWrite", "cache_write")
	result.Usage.Reasoning += intValue(usage, "reasoning")
	result.Usage.TotalTokens += intValue(usage, "totalTokens", "total_tokens")
	if cost, ok := usage["cost"].(float64); ok {
		result.Usage.Cost += cost
	} else if costs, ok := usage["cost"].(map[string]any); ok {
		parsed := harness.Cost{Input: floatValue(costs, "input"), Output: floatValue(costs, "output"), CacheRead: floatValue(costs, "cacheRead", "cache_read"), CacheWrite: floatValue(costs, "cacheWrite", "cache_write"), Reasoning: floatValue(costs, "reasoning"), Total: floatValue(costs, "total")}
		result.Usage.Costs.Input += parsed.Input
		result.Usage.Costs.Output += parsed.Output
		result.Usage.Costs.CacheRead += parsed.CacheRead
		result.Usage.Costs.CacheWrite += parsed.CacheWrite
		result.Usage.Costs.Reasoning += parsed.Reasoning
		result.Usage.Costs.Total += parsed.Total
		result.Usage.Cost += parsed.Total
	} else if cost, ok := event["cost"].(float64); ok {
		result.Usage.Cost += cost
	}
	stop := stringValue(message, "stopReason", "stop_reason")
	if stop != "aborted" && stop != "error" {
		result.ContextTokens = intValue(message, "contextTokens", "context_tokens")
		result.ContextWindow = intValue(message, "contextWindow", "context_window")
	}
}

func openRaw(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

func terminateGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	return -1
}

func emit(ctx context.Context, sink harness.EventSink, event harness.Event) {
	if sink != nil {
		_ = sink(ctx, event)
	}
}

func splitModel(value string) (string, string) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) == 1 {
		return "", value
	}
	return parts[0], parts[1]
}

func assistantText(event map[string]any) string {
	if text, ok := event["text"].(string); ok {
		return text
	}
	message, _ := event["message"].(map[string]any)
	if text, ok := message["content"].(string); ok {
		return text
	}
	if content, ok := message["content"].([]any); ok {
		var text strings.Builder
		for _, item := range content {
			part, _ := item.(map[string]any)
			if value, ok := part["text"].(string); ok {
				text.WriteString(value)
			}
		}
		return text.String()
	}
	return ""
}

func eventTime(event map[string]any, fallback time.Time) time.Time {
	for _, key := range []string{"timestamp", "startedAt", "endedAt"} {
		if value, ok := event[key].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func stringValue(values map[string]any, keys ...string) string {
	value, _ := firstValue(values, keys...).(string)
	return value
}

func boolValue(values map[string]any, keys ...string) bool {
	value, _ := firstValue(values, keys...).(bool)
	return value
}

func floatValue(values map[string]any, keys ...string) float64 {
	value, _ := firstValue(values, keys...).(float64)
	return value
}
func intValue(values map[string]any, keys ...string) int {
	switch value := firstValue(values, keys...).(type) {
	case float64:
		return int(value)
	case json.Number:
		n, _ := strconv.Atoi(value.String())
		return n
	case int:
		return value
	default:
		return 0
	}
}

func boundedJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return truncate(string(encoded), maxEventText)
}

func toolLabel(arguments any) string {
	values, _ := arguments.(map[string]any)
	return truncate(stringValue(values, "command", "path", "file_path", "pattern", "query", "url"), 160)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func displayCommand(path string, args []string) string {
	parts := append([]string{path}, args...)
	for i, part := range parts {
		if strings.ContainsAny(part, " \t\n") {
			parts[i] = strconv.Quote(part)
		}
	}
	return strings.Join(parts, " ")
}

type tailWriter struct {
	data  []byte
	limit int
}

func (w *tailWriter) Write(data []byte) (int, error) {
	w.data = append(w.data, data...)
	if len(w.data) > w.limit {
		w.data = w.data[len(w.data)-w.limit:]
	}
	return len(data), nil
}

func (w *tailWriter) String() string { return strings.TrimSpace(string(w.data)) }

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

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
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
	args = append(args, request.Prompt)

	// Pi receives an explicit session ID and directory, so a started process
	// owns an initialized session. Failures before Start leave the reserved
	// session untouched.
	notStarted := harness.Result{SessionID: request.SessionID, Provider: provider, Model: model}
	cmd := exec.Command(h.Path, args...)
	cmd.Dir = request.CWD
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return notStarted, fmt.Errorf("pi stdout: %w", err)
	}
	stderr := &tailWriter{limit: maxStderr}
	cmd.Stderr = stderr
	if err := ensureOutputPaths(request); err != nil {
		return notStarted, err
	}
	raw, err := openRaw(request.RawOutputPath)
	if err != nil {
		return notStarted, err
	}
	if raw != nil {
		defer raw.Close()
	}
	if err := cmd.Start(); err != nil {
		return notStarted, fmt.Errorf("start pi: %w", err)
	}
	started := time.Now()
	result := harness.Result{
		SessionID:          request.SessionID,
		Provider:           provider,
		Model:              model,
		SessionReady:       true,
		AccountingComplete: true,
	}
	if err := emit(parent, sink, session.NewProcessStart(session.ProcessStartPayload{PID: cmd.Process.Pid, Command: displayCommand(h.Path, args)})); err != nil {
		terminateGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		return result, fmt.Errorf("emit pi process start: %w", err)
	}

	terminated := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateGroup(cmd.Process.Pid)
		case <-terminated:
		}
	}()

	consumed, scanErr := consume(stdout, raw, sink, parent)
	consumed.SessionID = result.SessionID
	consumed.Provider = result.Provider
	consumed.Model = result.Model
	consumed.SessionReady = result.SessionReady
	consumed.AccountingComplete = result.AccountingComplete
	result = consumed
	waitErr := cmd.Wait()
	close(terminated)
	result.ExitCode = exitCode(waitErr)
	if err := emit(parent, sink, session.NewProcessEnd(session.ProcessEndPayload{PID: cmd.Process.Pid, ExitCode: result.ExitCode, DurationMS: time.Since(started).Milliseconds()})); err != nil && scanErr == nil {
		scanErr = fmt.Errorf("emit process end: %w", err)
	}
	if scanErr != nil {
		result.AccountingComplete = false
		return result, fmt.Errorf("read pi output: %w", scanErr)
	}
	if ctx.Err() != nil {
		result.AccountingComplete = false
		return result, fmt.Errorf("pi interrupted: %w", ctx.Err())
	}
	if waitErr != nil && strings.TrimSpace(result.Text) == "" {
		result.AccountingComplete = false
		return result, fmt.Errorf("pi exited %d: %s", result.ExitCode, stderr.String())
	}
	return result, nil
}

func ensureOutputPaths(request harness.Request) error {
	for _, path := range []string{request.SessionDirectory, filepath.Dir(request.RawOutputPath)} {
		if path == "" || path == "." {
			continue
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
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
		if err := processEvent(event, toolStarts, &result, sink, ctx); err != nil {
			return result, err
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	for id, start := range toolStarts {
		if start.folded {
			continue
		}
		entry := session.NewToolCall(session.ToolCallPayload{
			ToolCallID: id,
			Tool:       start.name,
			Arguments:  session.BoundedJSON(start.arguments),
			Incomplete: true,
			StartedAt:  &start.startedAt,
		})
		if err := emit(ctx, sink, entry); err != nil {
			return result, err
		}
	}
	return result, nil
}

type toolStart struct {
	name      string
	arguments any
	startedAt time.Time
	folded    bool
}

func processEvent(event map[string]any, tools map[string]toolStart, result *harness.Result, sink harness.EventSink, ctx context.Context) error {
	typeName, _ := event["type"].(string)
	switch typeName {
	case "message_start", "message_update", "tool_execution_update":
		return nil
	case "tool_execution_start":
		id := stringValue(event, "toolCallId", "tool_call_id")
		tools[id] = toolStart{name: stringValue(event, "toolName", "tool_name", "name"), arguments: firstValue(event, "args", "arguments"), startedAt: eventTime(event, time.Now())}
		return nil
	case "tool_execution_end":
		id := stringValue(event, "toolCallId", "tool_call_id")
		start := tools[id]
		if start.folded {
			return nil
		}
		ended := eventTime(event, time.Now())
		success := !boolValue(event, "isError", "error")
		start.folded = true
		tools[id] = start
		return emit(ctx, sink, session.NewToolCall(session.ToolCallPayload{ToolCallID: id, Tool: start.name, Arguments: session.BoundedJSON(start.arguments), Result: session.Truncate(fmt.Sprint(firstValue(event, "result", "output")), maxEventText), Success: &success, StartedAt: &start.startedAt, EndedAt: &ended, DurationMS: ended.Sub(start.startedAt).Milliseconds()}))
	case "message_end":
		return processMessageEnd(event, tools, result, sink, ctx)
	default:
		return emit(ctx, sink, session.NewCustom(session.CustomPayload{CustomType: customType(typeName), Data: session.BoundedJSON(event)}))
	}
}

func processMessageEnd(event map[string]any, tools map[string]toolStart, result *harness.Result, sink harness.EventSink, ctx context.Context) error {
	message, _ := event["message"].(map[string]any)
	role := stringValue(message, "role")
	if role == "" {
		role = stringValue(event, "role")
	}
	if strings.EqualFold(role, "toolResult") || strings.EqualFold(role, "tool_result") {
		id := stringValue(message, "toolCallId", "tool_call_id")
		if id == "" {
			id = stringValue(event, "toolCallId", "tool_call_id")
		}
		start := tools[id]
		if start.folded {
			return nil
		}
		ended := eventTime(event, time.Now())
		success := !boolValue(message, "isError", "is_error", "error") && !boolValue(event, "isError", "is_error", "error")
		start.folded = true
		tools[id] = start
		return emit(ctx, sink, session.NewToolCall(session.ToolCallPayload{ToolCallID: id, Tool: start.name, Arguments: session.BoundedJSON(start.arguments), Result: session.Truncate(toolResultText(event), maxEventText), Success: &success, StartedAt: &start.startedAt, EndedAt: &ended, DurationMS: ended.Sub(start.startedAt).Milliseconds()}))
	}

	text := assistantText(event)
	usage := messageUsage(event)
	stopReason := stringValue(message, "stopReason", "stop_reason")
	model := stringValue(message, "model")
	switch strings.ToLower(role) {
	case "assistant":
		if strings.TrimSpace(text) != "" {
			result.Text = text
		}
		accumulateUsage(event, result)
		return emit(ctx, sink, session.NewMessage(session.MessagePayload{Role: "assistant", Text: text, StopReason: stopReason, Model: model, Usage: usage}))
	case "user", "system":
		return emit(ctx, sink, session.NewMessage(session.MessagePayload{Role: strings.ToLower(role), Text: text, StopReason: stopReason, Model: model, Usage: usage}))
	default:
		return emit(ctx, sink, session.NewCustom(session.CustomPayload{CustomType: "message_end", Data: session.BoundedJSON(event)}))
	}
}

func messageUsage(event map[string]any) *session.Usage {
	message, _ := event["message"].(map[string]any)
	usage, _ := firstValue(message, "usage").(map[string]any)
	if usage == nil {
		usage, _ = event["usage"].(map[string]any)
	}
	if usage == nil {
		return nil
	}
	return &session.Usage{Input: intValue(usage, "input"), Output: intValue(usage, "output"), CacheRead: intValue(usage, "cacheRead", "cache_read"), CacheWrite: intValue(usage, "cacheWrite", "cache_write"), Reasoning: intValue(usage, "reasoning"), TotalTokens: intValue(usage, "totalTokens", "total_tokens")}
}

func toolResultText(event map[string]any) string {
	message, _ := event["message"].(map[string]any)
	if value := firstValue(message, "content", "result", "output"); value != nil {
		if text, ok := value.(string); ok {
			return text
		}
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return fmt.Sprint(firstValue(event, "result", "output"))
}

func customType(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
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
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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

func emit(ctx context.Context, sink harness.EventSink, event harness.Event) error {
	if sink != nil {
		return sink(ctx, event)
	}
	return nil
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

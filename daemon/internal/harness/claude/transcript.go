// Package claude implements the Claude Code harness: saved-transcript and
// live stream-json decoders feeding the daemon-owned session contract, plus
// process execution with durable native session lifecycle.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

const maxTranscriptLine = 4 << 20

// TranscriptSummary is the result of replaying a saved transcript. Cost is
// always unknown from a transcript alone; accounting is incomplete.
type TranscriptSummary struct {
	Usage UsageAccumulator
	Model string
}

// UsageAccumulator deduplicates per-message usage.
type UsageAccumulator struct {
	session.Usage
	seenMessages map[string]bool
}

func (a *UsageAccumulator) add(messageID string, usage session.Usage) {
	if a.seenMessages == nil {
		a.seenMessages = map[string]bool{}
	}
	key := messageID
	if key == "" {
		key = fmt.Sprintf("anonymous-%d", len(a.seenMessages))
	}
	if a.seenMessages[key] {
		return
	}
	a.seenMessages[key] = true
	a.Input += usage.Input
	a.Output += usage.Output
	a.CacheRead += usage.CacheRead
	a.CacheWrite += usage.CacheWrite
	a.Reasoning += usage.Reasoning
	a.TotalTokens += usage.TotalTokens
}

type pendingTool struct {
	id        string
	name      string
	arguments any
	scope     string
	startedAt time.Time
}

// ReplayTranscript decodes saved-transcript JSONL (pinned to the community
// JSONL schema revision) into session contract events. It is test support for
// fixture verification and task-owned replay, not a public history importer.
func ReplayTranscript(ctx context.Context, r io.Reader, sink harness.EventSink) (TranscriptSummary, error) {
	var summary TranscriptSummary
	seenUUID := map[string]bool{}
	pending := map[string]pendingTool{}
	textBuffer := map[string]*strings.Builder{}
	textOrder := []string{}
	messageUsageSeen := map[string]bool{}

	emit := func(event harness.Event) error {
		if sink == nil {
			return nil
		}
		return sink(ctx, event)
	}

	flushText := func(key string) error {
		builder := textBuffer[key]
		if builder == nil {
			return nil
		}
		text := builder.String()
		delete(textBuffer, key)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		role, model, stop := parseTextKey(key)
		return emit(session.NewMessage(session.MessagePayload{Role: role, Text: text, StopReason: stop, Model: model}))
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if uuid, _ := record["uuid"].(string); uuid != "" {
			if seenUUID[uuid] {
				continue
			}
			seenUUID[uuid] = true
		}
		recordType, _ := record["type"].(string)
		switch recordType {
		case "user":
			if err := replayUser(record, pending, textBuffer, &textOrder, emit); err != nil {
				return summary, err
			}
		case "assistant":
			if err := replayAssistant(record, pending, textBuffer, &textOrder, &summary, messageUsageSeen, emit); err != nil {
				return summary, err
			}
		case "system":
			if err := replaySystem(record, emit); err != nil {
				return summary, err
			}
		case "progress", "sidechain":
			emitCustom(recordType, record, emit)
		case "permission-mode", "attachment", "file-history-snapshot", "custom-title", "agent-name", "last-prompt":
			emitCustom("claude."+recordType, record, emit)
		case "summary", "queue-operation":
			emitCustom("claude."+recordType, record, emit)
		case "":
			emitCustom("claude.unknown", record, emit)
		default:
			emitCustom("claude."+recordType, record, emit)
		}
	}
	if err := scanner.Err(); err != nil {
		return summary, err
	}
	for _, key := range textOrder {
		if _, ok := textBuffer[key]; ok {
			if err := flushText(key); err != nil {
				return summary, err
			}
		}
	}
	// Unmatched tool uses become incomplete calls; never infer success.
	for _, tool := range pending {
		if err := emit(session.NewToolCall(session.ToolCallPayload{
			ToolCallID: tool.id,
			Tool:       tool.name,
			Arguments:  session.BoundedJSON(tool.arguments),
			Incomplete: true,
			StartedAt:  &tool.startedAt,
		})); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func parseTextKey(key string) (role, model, stop string) {
	parts := strings.SplitN(key, "\x00", 4)
	if len(parts) == 4 {
		return parts[1], parts[2], parts[3]
	}
	return "assistant", "", ""
}

func textKey(scope, role, model, stop, messageID string) string {
	return strings.Join([]string{scope, role, model, stop, messageID}, "\x00")
}

func replayUser(record map[string]any, pending map[string]pendingTool, textBuffer map[string]*strings.Builder, order *[]string, emit func(harness.Event) error) error {
	if isMeta, _ := record["isMeta"].(bool); isMeta {
		emitCustom("claude.meta_user", record, emit)
		return nil
	}
	message, _ := record["message"].(map[string]any)
	if message == nil {
		message = record
	}
	content := message["content"]
	if content == nil {
		content = record["content"]
	}
	scope := agentScope(record)
	blocks := normalizeBlocks(content)
	var textParts []string
	for _, b := range blocks {
		switch b.kind {
		case "text":
			textParts = append(textParts, b.text)
		case "tool_result":
			toolID := b.toolID
			key := scope + "\x00" + toolID
			if tool, ok := pending[key]; ok {
				delete(pending, key)
				ended := time.Now()
				if timestamp := parseTime(record["timestamp"]); timestamp != nil {
					ended = *timestamp
				}
				success := !b.isError
				if err := emit(session.NewToolCall(session.ToolCallPayload{
					ToolCallID: toolID,
					Tool:       tool.name,
					Arguments:  session.BoundedJSON(tool.arguments),
					Result:     session.Truncate(b.text, maxText),
					Success:    &success,
					StartedAt:  &tool.startedAt,
					EndedAt:    &ended,
					DurationMS: ended.Sub(tool.startedAt).Milliseconds(),
				})); err != nil {
					return err
				}
			} else {
				emitCustom("claude.orphan_tool_result", map[string]any{"tool_use_id": toolID, "text": session.Truncate(b.text, 1024)}, emit)
			}
		case "other":
			// Mixed non-tool content is preserved as user text.
			textParts = append(textParts, b.text)
		}
	}
	// Preserve non-tool text when mixed with results.
	if text := strings.Join(textParts, "\n"); strings.TrimSpace(text) != "" {
		return emit(session.NewMessage(session.MessagePayload{Role: "user", Text: text}))
	}
	return nil
}

func replayAssistant(record map[string]any, pending map[string]pendingTool, textBuffer map[string]*strings.Builder, order *[]string, summary *TranscriptSummary, usageSeen map[string]bool, emit func(harness.Event) error) error {
	message, _ := record["message"].(map[string]any)
	if message == nil {
		message = record
	}
	if isSidechain(record, message) {
		emitCustom("claude.sidechain", record, emit)
		return nil
	}
	content := message["content"]
	scope := agentScope(record)
	blocks := normalizeBlocks(content)
	model, _ := message["model"].(string)
	if model == "" {
		model, _ = record["model"].(string)
	}
	if model != "" && summary.Model == "" {
		summary.Model = model
	}
	stop, _ := message["stop_reason"].(string)
	if stop == "" {
		stop, _ = record["stop_reason"].(string)
	}
	messageID, _ := message["id"].(string)
	if messageID == "" {
		messageID, _ = record["message_id"].(string)
	}
	usageKey := scope + "\x00" + messageID
	if rawUsage, ok := message["usage"].(map[string]any); ok && !usageSeen[usageKey] {
		usageSeen[usageKey] = true
		summary.Usage.add(usageKey, transcriptUsage(rawUsage))
	}
	startedAt := time.Now()
	if timestamp := parseTime(record["timestamp"]); timestamp != nil {
		startedAt = *timestamp
	}
	for _, b := range blocks {
		switch b.kind {
		case "text":
			key := textKey(scope, "assistant", model, stop, messageID)
			builder, ok := textBuffer[key]
			if !ok {
				builder = &strings.Builder{}
				textBuffer[key] = builder
				*order = append(*order, key)
			}
			// Final complete snapshot wins over repeated prefixes: reset when
			// the new fragment extends the buffered prefix.
			if current := builder.String(); current != "" && strings.HasPrefix(b.text, current) {
				builder.Reset()
			}
			if !strings.Contains(builder.String(), b.text) {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(b.text)
			}
		case "tool_use":
			if b.toolID == "" {
				continue
			}
			pending[scope+"\x00"+b.toolID] = pendingTool{id: b.toolID, name: b.toolName, arguments: b.toolInput, scope: scope, startedAt: startedAt}
		case "other":
			emitCustom("claude.assistant_block", map[string]any{"text": b.text}, emit)
		}
	}
	return nil
}

func replaySystem(record map[string]any, emit func(harness.Event) error) error {
	subtype, _ := record["subtype"].(string)
	content := record["content"]
	text := ""
	switch value := content.(type) {
	case string:
		text = value
	case []any:
		for _, item := range stripThinking(value) {
			if part, ok := item.(map[string]any); ok {
				if t, ok := part["text"].(string); ok {
					text += t
				}
			}
		}
	}
	switch subtype {
	case "", "local_command":
		if strings.TrimSpace(text) == "" && subtype == "" {
			emitCustom("claude.system", record, emit)
			return nil
		}
		return emit(session.NewMessage(session.MessagePayload{Role: "system", Text: text}))
	default:
		emitCustom("claude.system."+subtype, record, emit)
		return nil
	}
}

func agentScope(record map[string]any) string {
	if agentID, _ := record["agentId"].(string); agentID != "" {
		return agentID
	}
	if parent, _ := record["parentToolUseID"].(string); parent != "" {
		return parent
	}
	if source, _ := record["sourceToolAssistantUUID"].(string); source != "" {
		return source
	}
	if sessionID, _ := record["sessionId"].(string); sessionID != "" {
		return sessionID
	}
	return "main"
}

func isSidechain(record, message map[string]any) bool {
	if value, ok := record["isSidechain"].(bool); ok && value {
		return true
	}
	if value, ok := message["isSidechain"].(bool); ok && value {
		return true
	}
	if parent, _ := record["parentToolUseID"].(string); parent != "" {
		return true
	}
	return false
}

func emitCustom(customType string, data any, emit func(harness.Event) error) {
	_ = emit(session.NewCustom(session.CustomPayload{CustomType: customType, Data: session.BoundedJSON(scrubThinking(data))}))
}

// scrubThinking recursively removes recognized thinking/redacted-thinking
// blocks from custom data before bounding.
func scrubThinking(data any) any {
	switch value := data.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(value))
		for key, entry := range value {
			cleaned[key] = scrubThinking(entry)
		}
		return cleaned
	case []any:
		var kept []any
		for _, item := range value {
			if part, ok := item.(map[string]any); ok {
				if kind, _ := part["type"].(string); kind == "thinking" || kind == "redacted_thinking" {
					continue
				}
			}
			kept = append(kept, scrubThinking(item))
		}
		return kept
	default:
		return data
	}
}

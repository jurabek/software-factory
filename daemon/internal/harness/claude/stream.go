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

// StreamSummary is the terminal accounting for one live invocation.
type StreamSummary struct {
	harness.Result
	hadTerminalResult bool
}

// DecodeStream consumes live `claude -p --output-format stream-json --verbose`
// stdout. It emits normalized message/tool events for the main loop, scoped
// custom entries for subagents, bounded custom metadata for init, and returns
// the terminal result. It never tails the native transcript.
func DecodeStream(ctx context.Context, r io.Reader, sink harness.EventSink) (StreamSummary, error) {
	var summary StreamSummary
	summary.AccountingComplete = true
	seenRecords := map[string]bool{}
	seenToolIDs := map[string]bool{}
	pending := map[string]pendingTool{}
	knownInput := 0
	knownCacheRead := 0
	knownCacheWrite := 0

	emit := func(event harness.Event) error {
		if sink == nil {
			return nil
		}
		return sink(ctx, event)
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
			return summary, fmt.Errorf("decode stream record: %w", err)
		}
		recordType, _ := record["type"].(string)
		switch recordType {
		case "system":
			subtype, _ := record["subtype"].(string)
			if subtype == "init" {
				sessionID, _ := record["session_id"].(string)
				if sessionID != "" {
					summary.SessionID = sessionID
					summary.SessionReady = true
				}
				if model, _ := record["model"].(string); model != "" {
					summary.Model = model
				}
				emitCustom("claude.init", record, emit)
				continue
			}
			emitCustom("claude.system."+orDefault(subtype, "message"), record, emit)
		case "stream_event":
			// Partial deltas are never emitted.
			continue
		case "assistant", "user":
			if parent, _ := record["parent_tool_use_id"].(string); parent != "" {
				emitCustom("claude.subagent", record, emit)
				continue
			}
			message, _ := record["message"].(map[string]any)
			if message == nil {
				message = record
			}
			messageID, _ := message["id"].(string)
			if messageID == "" {
				messageID, _ = record["id"].(string)
			}
			recordUUID, _ := record["uuid"].(string)
			dedupeKey := recordUUID
			if dedupeKey == "" {
				dedupeKey = recordType + "\x00" + messageID + "\x00" + string(session.BoundedJSON(message["content"]))
			}
			if seenRecords[dedupeKey] {
				continue
			}
			seenRecords[dedupeKey] = true
			content := message["content"]
			if content == nil {
				content = record["content"]
			}
			blocks := normalizeBlocks(content)
			model, _ := message["model"].(string)
			if model == "" {
				model, _ = record["model"].(string)
			}
			if model != "" && summary.Model == "" {
				summary.Model = model
			}
			stop, _ := message["stop_reason"].(string)
			received := time.Now()
			for _, b := range blocks {
				switch b.kind {
				case "text":
					if recordType == "assistant" {
						if err := emit(session.NewMessage(session.MessagePayload{Role: "assistant", Text: b.text, StopReason: stop, Model: model})); err != nil {
							return summary, err
						}
					} else {
						if err := emit(session.NewMessage(session.MessagePayload{Role: "user", Text: b.text})); err != nil {
							return summary, err
						}
					}
				case "tool_use":
					if b.toolID == "" || seenToolIDs[b.toolID] {
						continue
					}
					seenToolIDs[b.toolID] = true
					pending[b.toolID] = pendingTool{id: b.toolID, name: b.toolName, arguments: b.toolInput, startedAt: received}
				case "tool_result":
					tool, ok := pending[b.toolID]
					if !ok {
						emitCustom("claude.orphan_tool_result", map[string]any{"tool_use_id": b.toolID}, emit)
						continue
					}
					delete(pending, b.toolID)
					success := !b.isError
					if err := emit(session.NewToolCall(session.ToolCallPayload{
						ToolCallID: b.toolID,
						Tool:       tool.name,
						Arguments:  session.BoundedJSON(tool.arguments),
						Result:     session.Truncate(b.text, maxText),
						Success:    &success,
						StartedAt:  &tool.startedAt,
						EndedAt:    &received,
						DurationMS: received.Sub(tool.startedAt).Milliseconds(),
					})); err != nil {
						return summary, err
					}
				case "other":
					emitCustom("claude.block", map[string]any{"text": b.text}, emit)
				}
			}
			// Live per-message usage is often a placeholder; retain only
			// credible input/cache counts for crash accounting.
			if rawUsage, ok := message["usage"].(map[string]any); ok {
				usage := transcriptUsage(rawUsage)
				knownInput += usage.Input
				knownCacheRead += usage.CacheRead
				knownCacheWrite += usage.CacheWrite
			}
		case "result":
			summary.hadTerminalResult = true
			if err := decodeTerminalResult(record, &summary, emit); err != nil {
				return summary, err
			}
		default:
			emitCustom("claude."+orDefault(recordType, "unknown"), record, emit)
		}
	}
	if err := scanner.Err(); err != nil {
		summary.AccountingComplete = false
		return summary, err
	}
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
	if !summary.hadTerminalResult {
		summary.AccountingComplete = false
		summary.Usage.Input = knownInput
		summary.Usage.CacheRead = knownCacheRead
		summary.Usage.CacheWrite = knownCacheWrite
		return summary, fmt.Errorf("claude stream missing terminal result")
	}
	return summary, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// decodeTerminalResult interprets the terminal `result` record as the single
// execution outcome. Error results, session mismatches, and malformed records
// are errors even when text exists.
func decodeTerminalResult(record map[string]any, summary *StreamSummary, emit func(harness.Event) error) error {
	subtype, _ := record["subtype"].(string)
	isError := boolField(record, "is_error", "isError")
	sessionID, _ := record["session_id"].(string)
	if sessionID != "" {
		if summary.SessionID != "" && sessionID != summary.SessionID {
			return fmt.Errorf("harness session identity changed from %s to %s", summary.SessionID, sessionID)
		}
		summary.SessionID = sessionID
		summary.SessionReady = true
	}
	if subtype == "error" || isError {
		emitCustom("claude.error_result", record, emit)
		applyTerminalUsage(record, summary)
		summary.AccountingComplete = false
		return fmt.Errorf("claude error result: %s", boundedText(record["result"]))
	}
	text := boundedText(record["result"])
	if strings.TrimSpace(text) == "" {
		emitCustom("claude.empty_result", record, emit)
		summary.AccountingComplete = false
		return fmt.Errorf("claude terminal result is empty")
	}
	summary.Text = text
	if model, _ := record["model"].(string); model != "" {
		summary.Model = model
	}
	applyTerminalUsage(record, summary)
	// Emit the terminal text once when no equivalent assistant text exists.
	// Callers deduplicate by tracking emitted assistant text; here we always
	// return Text and let Run decide emission. Emit a bounded custom marker
	// only for explicit permission denials.
	if subtype == "permission_denied" || strings.Contains(strings.ToLower(text), "permission") && isError {
		emitCustom("claude.permission_denial", record, emit)
	}
	return nil
}

// applyTerminalUsage prefers whole-tree modelUsage totals (includes
// subagents) over root usage (excludes subagents), never both. Terminal
// total_cost_usd is the single-invocation estimate.
func applyTerminalUsage(record map[string]any, summary *StreamSummary) {
	if modelUsage, ok := record["modelUsage"].(map[string]any); ok && len(modelUsage) > 0 {
		var total session.Usage
		for _, entry := range modelUsage {
			usageMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			total.Input += numberField(usageMap, "inputTokens", "input")
			total.Output += numberField(usageMap, "outputTokens", "output")
			total.CacheRead += numberField(usageMap, "cacheReadInputTokens", "cache_read")
			total.CacheWrite += numberField(usageMap, "cacheCreationInputTokens", "cache_write")
		}
		total.TotalTokens = total.Input + total.Output + total.CacheRead + total.CacheWrite
		summary.Usage.Input = total.Input
		summary.Usage.Output = total.Output
		summary.Usage.CacheRead = total.CacheRead
		summary.Usage.CacheWrite = total.CacheWrite
		summary.Usage.Reasoning = total.Reasoning
		summary.Usage.TotalTokens = total.TotalTokens
	} else if rawUsage, ok := record["usage"].(map[string]any); ok {
		usage := transcriptUsage(rawUsage)
		summary.Usage.Input = usage.Input
		summary.Usage.Output = usage.Output
		summary.Usage.CacheRead = usage.CacheRead
		summary.Usage.CacheWrite = usage.CacheWrite
		summary.Usage.Reasoning = usage.Reasoning
		summary.Usage.TotalTokens = usage.TotalTokens
	}
	summary.Usage.Cost = floatField(record, "total_cost_usd", "totalCostUsd")
	if summary.Usage.Cost == 0 {
		summary.AccountingComplete = false
	}
}

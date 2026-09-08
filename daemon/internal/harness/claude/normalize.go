package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

const maxText = 16 << 10

// block is a normalized content block after thinking exclusion.
type block struct {
	kind      string // text, tool_use, tool_result, other
	text      string
	toolID    string
	toolName  string
	toolInput any
	isError   bool
}

func stringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func boolField(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
}

func numberField(values map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			n, _ := value.Int64()
			return int(n)
		}
	}
	return 0
}

func floatField(values map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := values[key].(float64); ok {
			return value
		}
	}
	return 0
}

// stripThinking removes recognized thinking/redacted-thinking blocks from a
// content array before any bounding/preview path.
func stripThinking(content []any) []any {
	kept := content[:0]
	for _, item := range content {
		part, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		kind, _ := part["type"].(string)
		if kind == "thinking" || kind == "redacted_thinking" {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// normalizeBlocks converts a message content value (string or block array)
// into normalized blocks with thinking excluded.
func normalizeBlocks(content any) []block {
	switch value := content.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []block{{kind: "text", text: value}}
	case []any:
		var out []block
		for _, item := range stripThinking(value) {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := part["type"].(string)
			switch kind {
			case "text":
				if text, _ := part["text"].(string); text != "" {
					out = append(out, block{kind: "text", text: text})
				}
			case "tool_use":
				id, _ := part["id"].(string)
				name, _ := part["name"].(string)
				out = append(out, block{kind: "tool_use", toolID: id, toolName: name, toolInput: part["input"]})
			case "tool_result":
				id, _ := part["tool_use_id"].(string)
				out = append(out, block{kind: "tool_result", toolID: id, text: toolResultString(part), isError: toolResultIsError(part)})
			case "thinking", "redacted_thinking":
				continue
			default:
				// Preserve unknown block shapes as bounded text only via custom path.
				out = append(out, block{kind: "other", text: string(session.BoundedJSON(part))})
			}
		}
		return out
	default:
		return nil
	}
}

func toolResultString(part map[string]any) string {
	content, ok := part["content"]
	if !ok {
		return ""
	}
	return boundedText(content)
}

func toolResultIsError(part map[string]any) bool {
	if value, ok := part["is_error"].(bool); ok {
		return value
	}
	return boolField(part, "isError")
}

// boundedText renders result content (string, object, or block array) as
// deterministic bounded text, supporting stdout/stderr/interrupted fields and
// structured Agent results.
func boundedText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return session.Truncate(value, maxText)
	case []any:
		var parts []string
		for _, item := range value {
			switch entry := item.(type) {
			case string:
				parts = append(parts, entry)
			case map[string]any:
				if kind, _ := entry["type"].(string); kind == "thinking" || kind == "redacted_thinking" {
					continue
				}
				if text, ok := entry["text"].(string); ok {
					parts = append(parts, text)
					continue
				}
				parts = append(parts, string(session.BoundedJSON(entry)))
			default:
				parts = append(parts, fmt.Sprint(entry))
			}
		}
		return session.Truncate(strings.Join(parts, "\n"), maxText)
	case map[string]any:
		// Structured results: prefer stdout/stderr/interrupted fields.
		_, hasStdout := value["stdout"]
		_, hasStderr := value["stderr"]
		_, hasInterrupted := value["interrupted"]
		if hasStdout || hasStderr || hasInterrupted {
			var parts []string
			if stdout, _ := value["stdout"].(string); stdout != "" {
				parts = append(parts, stdout)
			}
			if stderr, _ := value["stderr"].(string); stderr != "" {
				parts = append(parts, stderr)
			}
			if interrupted, ok := value["interrupted"].(bool); ok && interrupted {
				parts = append(parts, "(interrupted)")
			}
			if len(parts) > 0 {
				return session.Truncate(strings.Join(parts, "\n"), maxText)
			}
		}
		return session.Truncate(string(session.BoundedJSON(value)), maxText)
	default:
		return session.Truncate(fmt.Sprint(value), maxText)
	}
}

// transcriptUsage maps usage objects with either snake_case or Claude
// transcript keys (input_tokens, output_tokens, cache_read_input_tokens,
// cache_creation_input_tokens) into session.Usage.
func transcriptUsage(usage map[string]any) session.Usage {
	input := numberField(usage, "input", "input_tokens")
	output := numberField(usage, "output", "output_tokens")
	cacheRead := numberField(usage, "cache_read", "cache_read_input_tokens")
	cacheWrite := numberField(usage, "cache_write", "cache_creation_input_tokens")
	return session.Usage{
		Input:       input,
		Output:      output,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		Reasoning:   numberField(usage, "reasoning"),
		TotalTokens: input + output + cacheRead + cacheWrite,
	}
}

func parseTime(value any) *time.Time {
	text, _ := value.(string)
	if text == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999Z07:00"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return &parsed
		}
	}
	return nil
}

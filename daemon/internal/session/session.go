// Package session defines the versioned, harness-independent event contract.
package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	FormatVersion   = 2
	MaxJSONBytes    = 16 << 10
	maxTargetBytes  = 160
	maxPreviewBytes = 180
)

type Kind string

const (
	KindMessage      Kind = "message"
	KindToolCall     Kind = "tool_call"
	KindProcessStart Kind = "process_start"
	KindProcessEnd   Kind = "process_end"
	KindPhaseStart   Kind = "phase_start"
	KindPhaseEnd     Kind = "phase_end"
	KindPlanFeedback Kind = "plan_feedback"
	KindTaskMessage  Kind = "task_message"
	KindCustom       Kind = "custom"
)

type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cache_read,omitempty"`
	CacheWrite  int `json:"cache_write,omitempty"`
	Reasoning   int `json:"reasoning,omitempty"`
	TotalTokens int `json:"total_tokens"`
}

type MessagePayload struct {
	Role       string `json:"role"`
	Text       string `json:"text"`
	StopReason string `json:"stop_reason,omitempty"`
	Model      string `json:"model,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
}

type ToolCallPayload struct {
	ToolCallID string          `json:"tool_call_id"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Result     string          `json:"result,omitempty"`
	Success    *bool           `json:"success,omitempty"`
	Incomplete bool            `json:"incomplete,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	EndedAt    *time.Time      `json:"ended_at,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
}

type ProcessStartPayload struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

type ProcessEndPayload struct {
	PID        int   `json:"pid"`
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms"`
}

type PhasePayload struct {
	Phase          string `json:"phase"`
	Name           string `json:"name,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Owner          string `json:"owner,omitempty"`
	Status         string `json:"status,omitempty"`
	Error          string `json:"error,omitempty"`
	InputSnapshot  string `json:"input_snapshot,omitempty"`
	OutputSnapshot string `json:"output_snapshot,omitempty"`
}

type PlanFeedbackPayload struct {
	Feedback   string `json:"feedback"`
	Actor      string `json:"actor,omitempty"`
	PlanDigest string `json:"plan_digest,omitempty"`
}

type TaskMessagePayload struct {
	MessageID      string `json:"message_id"`
	TaskID         string `json:"task_id"`
	Text           string `json:"text"`
	RecipientRole  string `json:"recipient_role"`
	AgentSessionID string `json:"agent_session_id"`
	TargetType     string `json:"target_type,omitempty"`
	TargetID       string `json:"target_id,omitempty"`
	DeliveryStatus string `json:"delivery_status"`
	FailureReason  string `json:"failure_reason,omitempty"`
}

type CustomPayload struct {
	CustomType string          `json:"custom_type"`
	Data       json.RawMessage `json:"data"`
}

type Display struct {
	Role       string `json:"role"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	Target     string `json:"target,omitempty"`
	Result     string `json:"result,omitempty"`
	Preview    string `json:"preview,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type Entry struct {
	Kind    Kind    `json:"kind"`
	Name    string  `json:"name,omitempty"`
	Payload any     `json:"payload"`
	Display Display `json:"display"`
	// NativeEntryID references the authoritative harness session entry this
	// event was derived from, when the harness exposes one.
	NativeEntryID string `json:"native_entry_id,omitempty"`
	// RequestID identifies the factory request whose native subtree produced
	// this event. It lets the read path resolve the exact native entry.
	RequestID string `json:"request_id,omitempty"`
}

func NewMessage(payload MessagePayload) Entry {
	return Entry{Kind: KindMessage, Payload: payload, Display: Describe(KindMessage, payload)}
}

func NewToolCall(payload ToolCallPayload) Entry {
	payload.Arguments = BoundedJSON(payload.Arguments)
	payload.Result = truncate(payload.Result, MaxJSONBytes)
	if payload.DurationMS == 0 && payload.StartedAt != nil && payload.EndedAt != nil {
		payload.DurationMS = payload.EndedAt.Sub(*payload.StartedAt).Milliseconds()
	}
	return Entry{Kind: KindToolCall, Name: payload.Tool, Payload: payload, Display: Describe(KindToolCall, payload)}
}

func NewProcessStart(payload ProcessStartPayload) Entry {
	return Entry{Kind: KindProcessStart, Payload: payload, Display: Describe(KindProcessStart, payload)}
}

func NewProcessEnd(payload ProcessEndPayload) Entry {
	return Entry{Kind: KindProcessEnd, Payload: payload, Display: Describe(KindProcessEnd, payload)}
}

func NewPhaseStart(payload PhasePayload) Entry {
	return Entry{Kind: KindPhaseStart, Name: payload.Name, Payload: payload, Display: Describe(KindPhaseStart, payload)}
}

func NewPhaseEnd(payload PhasePayload) Entry {
	return Entry{Kind: KindPhaseEnd, Name: payload.Name, Payload: payload, Display: Describe(KindPhaseEnd, payload)}
}

func NewTaskMessage(payload TaskMessagePayload) Entry {
	return Entry{Kind: KindTaskMessage, Payload: payload, Display: Describe(KindTaskMessage, payload)}
}

func NewCustom(payload CustomPayload) Entry {
	payload.Data = BoundedJSON(payload.Data)
	return Entry{Kind: KindCustom, Name: payload.CustomType, Payload: payload, Display: Describe(KindCustom, payload)}
}

// Describe derives the stable rendering metadata for an entry. Unknown kinds
// intentionally degrade to a neutral event rather than guessing semantics.
func Describe(kind Kind, payload any) Display {
	switch kind {
	case KindMessage:
		if value, ok := payloadValue[MessagePayload](payload); ok {
			role := "event"
			title := displayName(value.Role) + " message"
			switch strings.ToLower(value.Role) {
			case "user":
				role, title = "user", "User message"
			case "assistant":
				role, title = "agent", "Agent response"
			case "system":
				role, title = "system", "System message"
			}
			status := "neutral"
			if strings.EqualFold(value.StopReason, "error") || strings.EqualFold(value.StopReason, "aborted") {
				status = "failure"
			}
			if strings.EqualFold(value.Role, "assistant") {
				if summary, report, ok := envelopeHuman(value.Text); ok {
					display := Display{Role: role, Status: status, Title: title, Target: truncate(summary, maxTargetBytes), Result: truncate(report, MaxJSONBytes)}
					display.Preview = firstLine(summary, maxPreviewBytes)
					if display.Preview == "" {
						display.Preview = firstLine(report, maxPreviewBytes)
					}
					return display
				}
			}
			return withText(Display{Role: role, Status: status, Title: title}, value.Text)
		}
	case KindToolCall:
		if value, ok := payloadValue[ToolCallPayload](payload); ok {
			display := Display{Role: "tool", Status: "neutral", Title: toolTitle(value.Tool), Target: toolTarget(value.Arguments), Result: truncate(value.Result, MaxJSONBytes)}
			if value.Success != nil && !value.Incomplete {
				if *value.Success {
					display.Status = "success"
				} else {
					display.Status = "failure"
				}
			}
			display.Preview = firstLine(display.Result, maxPreviewBytes)
			display.DurationMS = value.DurationMS
			return display
		}
	case KindProcessStart:
		if value, ok := payloadValue[ProcessStartPayload](payload); ok {
			return withText(Display{Role: "event", Status: "neutral", Title: "Agent process started"}, value.Command)
		}
	case KindProcessEnd:
		if value, ok := payloadValue[ProcessEndPayload](payload); ok {
			status := "failure"
			if value.ExitCode == 0 {
				status = "success"
			}
			return Display{Role: "event", Status: status, Title: "Agent process finished", DurationMS: value.DurationMS}
		}
	case KindPhaseStart:
		if value, ok := payloadValue[PhasePayload](payload); ok {
			return Display{Role: "event", Status: "running", Title: "Attempt started", Target: truncate(value.Name, maxTargetBytes)}
		}
	case KindPhaseEnd:
		if value, ok := payloadValue[PhasePayload](payload); ok {
			return Display{Role: "event", Status: status(value.Status), Title: "Attempt finished", Target: truncate(value.Name, maxTargetBytes), Result: truncate(value.Error, MaxJSONBytes), Preview: firstLine(value.Error, maxPreviewBytes)}
		}
	case KindPlanFeedback:
		if value, ok := payloadValue[PlanFeedbackPayload](payload); ok {
			return withText(Display{Role: "user", Status: "neutral", Title: "Planner feedback"}, value.Feedback)
		}
	case KindTaskMessage:
		if value, ok := payloadValue[TaskMessagePayload](payload); ok {
			status := "neutral"
			if value.DeliveryStatus == "failed" {
				status = "failure"
			}
			return withText(Display{Role: "user", Status: status, Title: "Message " + value.DeliveryStatus, Target: value.RecipientRole}, value.Text)
		}
	case KindCustom:
		if value, ok := payloadValue[CustomPayload](payload); ok {
			text := string(value.Data)
			return Display{Role: "event", Status: "neutral", Title: displayName(value.CustomType), Result: text, Preview: firstLine(text, maxPreviewBytes)}
		}
	}

	text := string(BoundedJSON(payload))
	return Display{Role: "event", Status: "neutral", Title: displayName(string(kind)), Result: text, Preview: firstLine(text, maxPreviewBytes)}
}

func withText(display Display, text string) Display {
	display.Result = truncate(text, MaxJSONBytes)
	display.Preview = firstLine(display.Result, maxPreviewBytes)
	return display
}

// envelopeHuman extracts the human-readable summary and report from a
// deterministic agent envelope (planner/builder/reviewer JSON). It mirrors
// stage.object extraction so fenced or whitespace-padded envelopes still
// resolve to report_markdown instead of raw JSON.
func envelopeHuman(text string) (string, string, bool) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		first := strings.Index(trimmed, "\n")
		last := strings.LastIndex(trimmed, "```")
		if first >= 0 && last > first {
			trimmed = strings.TrimSpace(trimmed[first+1 : last])
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return "", "", false
	}
	var envelope struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Report  string `json:"report_markdown"`
	}
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &envelope); err != nil {
		return "", "", false
	}
	if strings.TrimSpace(envelope.Report) == "" {
		return "", "", false
	}
	return strings.TrimSpace(envelope.Summary), envelope.Report, true
}

func status(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success", "succeeded", "passed", "pass", "completed", "complete", "done":
		return "success"
	case "failure", "failed", "fail", "error", "aborted":
		return "failure"
	default:
		return "neutral"
	}
}

func payloadValue[T any](payload any) (T, bool) {
	if value, ok := payload.(T); ok {
		return value, true
	}
	value, ok := payload.(*T)
	if ok && value != nil {
		return *value, true
	}
	var zero T
	return zero, false
}

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

var toolTitles = map[string]string{
	"applypatch": "Edit",
	"bash":       "Bash",
	"edit":       "Edit",
	"glob":       "Files",
	"grep":       "Search",
	"read":       "Read",
	"webfetch":   "Web Fetch",
	"websearch":  "Web Search",
	"write":      "Write",
	"agent":      "Agent",
}

func toolTitle(name string) string {
	key := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
	if title, ok := toolTitles[key]; ok {
		return title
	}
	return displayName(name)
}

func displayName(value string) string {
	value = camelBoundary.ReplaceAllString(value, "$1 $2")
	value = strings.NewReplacer("_", " ", "-", " ").Replace(value)
	words := strings.Fields(value)
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func toolTarget(arguments json.RawMessage) string {
	var values map[string]any
	if json.Unmarshal(arguments, &values) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "command", "pattern", "query", "url"} {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncate(value, maxTargetBytes)
		}
	}
	return ""
}

func firstLine(value string, limit int) string {
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncate(line, limit)
		}
	}
	return ""
}

// Truncate keeps the beginning of a string and never splits a UTF-8 sequence.
// The returned string is at most limit bytes, including its marker.
func Truncate(value string, limit int) string {
	return truncate(value, limit)
}

func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	marker := "..."
	if limit <= len(marker) {
		return string([]rune(value)[:0]) + marker[:limit]
	}
	keep := limit - len(marker)
	for keep > 0 && !utf8.ValidString(value[:keep]) {
		keep--
	}
	return value[:keep] + marker
}

// BoundedJSON returns valid JSON no larger than the supplied limit, or
// MaxJSONBytes when no limit is supplied. Values that do not fit are
// represented by a valid object rather than a sliced JSON prefix.
func BoundedJSON(value any, limits ...int) json.RawMessage {
	limit := MaxJSONBytes
	if len(limits) > 0 {
		limit = limits[0]
	}
	return BoundedJSONLimit(value, limit)
}

// BoundedJSONLimit is the configurable form of BoundedJSON, useful for
// callers enforcing a smaller transport limit.
func BoundedJSONLimit(value any, limit int) json.RawMessage {
	if limit <= 0 {
		return json.RawMessage("null")
	}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= limit && json.Valid(encoded) {
		return json.RawMessage(encoded)
	}

	preview := ""
	if err == nil {
		preview = string(encoded)
	} else {
		preview = fmt.Sprint(value)
	}
	for {
		candidate, marshalErr := json.Marshal(struct {
			Truncated bool   `json:"truncated"`
			Preview   string `json:"preview"`
		}{true, preview})
		if marshalErr == nil && len(candidate) <= limit {
			return json.RawMessage(candidate)
		}
		if preview == "" {
			break
		}
		preview = truncate(preview, len(preview)/2)
	}
	// JSON has a four-byte minimum representation. Normal contract limits are
	// much larger; retain valid JSON for defensive callers with tiny limits.
	if limit >= 2 {
		return json.RawMessage("{}")
	}
	return json.RawMessage("null")
}

// Package session defines the versioned, harness-independent event contract.
package session

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	FormatVersion   = 1
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
	KindIntervention Kind = "intervention"
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

type InterventionPayload struct {
	Actor          string `json:"actor"`
	Intent         string `json:"intent"`
	Text           string `json:"text"`
	Delivery       string `json:"delivery"`
	InterventionID string `json:"intervention_id,omitempty"`
	TargetType     string `json:"target_type,omitempty"`
	TargetID       string `json:"target_id,omitempty"`
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
	Anchor         string `json:"anchor_json,omitempty"`
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

func NewIntervention(payload InterventionPayload) Entry {
	return Entry{Kind: KindIntervention, Payload: payload, Display: Describe(KindIntervention, payload)}
}

func NewPlanFeedback(payload PlanFeedbackPayload) Entry {
	return Entry{Kind: KindPlanFeedback, Payload: payload, Display: Describe(KindPlanFeedback, payload)}
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
	case KindIntervention:
		if value, ok := payloadValue[InterventionPayload](payload); ok {
			return withText(Display{Role: "user", Status: "neutral", Title: "Intervention " + displayName(value.Intent)}, value.Text)
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

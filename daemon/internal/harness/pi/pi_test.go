package pi

import (
	"context"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

func TestProcessEventNormalizesCompletedMessages(t *testing.T) {
	var events []session.Entry
	result := harness.Result{}
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	event := map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "assistant",
			"content":    "finished",
			"stopReason": "stop",
			"model":      "model-1",
			"usage":      map[string]any{"input": float64(10), "output": float64(4), "totalTokens": float64(14)},
		},
	}
	if err := processEvent(event, map[string]toolStart{}, &result, sink, context.Background()); err != nil {
		t.Fatal(err)
	}
	if result.Text != "finished" || result.Usage.TotalTokens != 14 {
		t.Fatalf("result = %#v", result)
	}
	if len(events) != 1 || events[0].Kind != session.KindMessage {
		t.Fatalf("events = %#v", events)
	}
	payload := events[0].Payload.(session.MessagePayload)
	if payload.Role != "assistant" || payload.Text != "finished" || payload.Usage == nil || payload.Usage.TotalTokens != 14 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestProcessEventDropsTransientAndCustomizesUnknown(t *testing.T) {
	var events []session.Entry
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	for _, event := range []map[string]any{{"type": "message_start"}, {"type": "message_update"}, {"type": "tool_execution_update"}, {"type": "future_entry", "value": "kept"}} {
		if err := processEvent(event, map[string]toolStart{}, &harness.Result{}, sink, context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 1 || events[0].Kind != session.KindCustom {
		t.Fatalf("events = %#v", events)
	}
	payload := events[0].Payload.(session.CustomPayload)
	if payload.CustomType != "future_entry" {
		t.Fatalf("custom type = %q", payload.CustomType)
	}
}

func TestToolCallIsFoldedExactlyOnceInEitherOrder(t *testing.T) {
	orders := []struct {
		name   string
		events []map[string]any
	}{
		{
			name: "message result first",
			events: []map[string]any{
				{"type": "tool_execution_start", "toolCallId": "call-1", "toolName": "read", "args": map[string]any{"path": "README.md"}},
				{"type": "message_end", "message": map[string]any{"role": "toolResult", "toolCallId": "call-1", "content": "contents"}},
				{"type": "tool_execution_end", "toolCallId": "call-1", "result": "contents"},
			},
		},
		{
			name: "execution result first",
			events: []map[string]any{
				{"type": "tool_execution_start", "toolCallId": "call-1", "toolName": "read", "args": map[string]any{"path": "README.md"}},
				{"type": "tool_execution_end", "toolCallId": "call-1", "result": "contents"},
				{"type": "message_end", "message": map[string]any{"role": "toolResult", "toolCallId": "call-1", "content": "contents"}},
			},
		},
	}
	for _, test := range orders {
		t.Run(test.name, func(t *testing.T) {
			var emitted []session.Entry
			sink := func(_ context.Context, event harness.Event) error {
				emitted = append(emitted, event)
				return nil
			}
			tools := map[string]toolStart{}
			for _, event := range test.events {
				if err := processEvent(event, tools, &harness.Result{}, sink, context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(emitted) != 1 || emitted[0].Kind != session.KindToolCall {
				t.Fatalf("events = %#v", emitted)
			}
			payload := emitted[0].Payload.(session.ToolCallPayload)
			if payload.ToolCallID != "call-1" || payload.Tool != "read" || payload.Success == nil || !*payload.Success {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}
}

func TestConsumeEmitsIncompleteToolAtEOF(t *testing.T) {
	input := `{"type":"tool_execution_start","toolCallId":"call-1","toolName":"edit","args":{"path":"main.go"}}` + "\n"
	var events []session.Entry
	result, err := consume(strings.NewReader(input), nil, func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" || len(events) != 1 {
		t.Fatalf("result = %#v, events = %#v", result, events)
	}
	payload := events[0].Payload.(session.ToolCallPayload)
	if !payload.Incomplete || payload.Success != nil {
		t.Fatalf("payload = %#v", payload)
	}
}

package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

func replayFile(t *testing.T, name string) ([]harness.Event, TranscriptSummary) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	summary, err := ReplayTranscript(context.Background(), strings.NewReader(string(data)), sink)
	if err != nil {
		t.Fatal(err)
	}
	return events, summary
}

func kinds(events []harness.Event) []session.Kind {
	out := make([]session.Kind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func TestTranscriptBasicMapping(t *testing.T) {
	events, summary := replayFile(t, "transcript_basic.jsonl")
	byKind := map[session.Kind]int{}
	for _, event := range events {
		byKind[event.Kind]++
	}
	if byKind[session.KindMessage] < 4 {
		t.Fatalf("messages = %d, want >=4: %v", byKind[session.KindMessage], kinds(events))
	}
	if byKind[session.KindToolCall] != 2 {
		t.Fatalf("tool_calls = %d, want 2 (one folded, one incomplete): %v", byKind[session.KindToolCall], kinds(events))
	}
	if byKind[session.KindCustom] < 4 {
		t.Fatalf("custom = %d, want >=4 (meta, turn_duration, title, orphan, progress, unknown): %v", byKind[session.KindCustom], kinds(events))
	}
	// Thinking must be omitted, visible text preserved.
	for _, event := range events {
		if event.Kind != session.KindMessage {
			continue
		}
		payload := event.Payload.(session.MessagePayload)
		if strings.Contains(payload.Text, "private plan") {
			t.Fatalf("thinking leaked into message: %q", payload.Text)
		}
	}
	// Folded tool call succeeds; unmatched is incomplete with unknown success.
	var folded, incomplete int
	for _, event := range events {
		if event.Kind != session.KindToolCall {
			continue
		}
		payload := event.Payload.(session.ToolCallPayload)
		if payload.Incomplete {
			incomplete++
			if payload.Success != nil {
				t.Fatalf("incomplete tool success = %v, want nil", *payload.Success)
			}
		} else {
			folded++
			if payload.Success == nil || !*payload.Success {
				t.Fatalf("folded tool success missing: %#v", payload)
			}
		}
	}
	if folded != 1 || incomplete != 1 {
		t.Fatalf("folded=%d incomplete=%d, want 1/1", folded, incomplete)
	}
	// Usage deduplicated per message id: msg-1 (100+20+10+5=135) + msg-3 (120+30+10+5=165) = 300.
	if summary.Usage.TotalTokens != 300 {
		t.Fatalf("total tokens = %d, want 300: %#v", summary.Usage.TotalTokens, summary.Usage)
	}
	if summary.Usage.CacheRead != 20 || summary.Usage.CacheWrite != 10 {
		t.Fatalf("cache = %+v, want read 20 write 10", summary.Usage)
	}
}

func TestTranscriptDedupesUUIDAndPartialSnapshots(t *testing.T) {
	input := `{"type":"assistant","uuid":"dup","sessionId":"s","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":"stop"}}
{"type":"assistant","uuid":"dup","sessionId":"s","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":"stop"}}
{"type":"assistant","uuid":"part-1","sessionId":"s","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Hel"}],"stop_reason":"stop"}}
{"type":"assistant","uuid":"part-2","sessionId":"s","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Hello world"}],"stop_reason":"stop"}}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	summary, err := ReplayTranscript(context.Background(), strings.NewReader(input), sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (dedupe uuid, collapse prefix): %v", len(events), kinds(events))
	}
	if summary.Usage.TotalTokens != 0 {
		t.Fatalf("usage = %#v, want zero without usage fields", summary.Usage)
	}
}

func TestTranscriptStructuredAndInterruptedResults(t *testing.T) {
	input := `{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test"}}]}}
{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":{"stdout":"ok","stderr":"","interrupted":true}}]}}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	if _, err := ReplayTranscript(context.Background(), strings.NewReader(input), sink); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != session.KindToolCall {
		t.Fatalf("events = %v", kinds(events))
	}
	payload := events[0].Payload.(session.ToolCallPayload)
	if !strings.Contains(payload.Result, "ok") || !strings.Contains(payload.Result, "interrupted") {
		t.Fatalf("result = %q, want stdout + interrupted", payload.Result)
	}
}

func TestTranscriptNestedThinkingScrubbedFromCustom(t *testing.T) {
	input := `{"type":"progress","uuid":"p1","sessionId":"s","content":[{"type":"thinking","thinking":"secret","signature":"sig"},{"type":"text","text":"working"}]}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	if _, err := ReplayTranscript(context.Background(), strings.NewReader(input), sink); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != session.KindCustom {
		t.Fatalf("events = %v", kinds(events))
	}
	encoded := string(events[0].Payload.(session.CustomPayload).Data)
	if strings.Contains(encoded, "secret") || strings.Contains(encoded, "sig") {
		t.Fatalf("thinking leaked into custom: %s", encoded)
	}
}

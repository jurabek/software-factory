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

func decodeFile(t *testing.T, name string) ([]harness.Event, StreamSummary, error) {
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
	summary, err := DecodeStream(context.Background(), strings.NewReader(string(data)), sink)
	return events, summary, err
}

func TestStreamBasicMapping(t *testing.T) {
	events, summary, err := decodeFile(t, "stream_basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if summary.SessionID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("session = %q", summary.SessionID)
	}
	if !summary.SessionReady {
		t.Fatal("session_ready = false, want true from init")
	}
	if summary.Text != "All checks pass." {
		t.Fatalf("text = %q", summary.Text)
	}
	// Whole-tree modelUsage wins over root usage: 200+50+20+10=280.
	if summary.Usage.TotalTokens != 280 {
		t.Fatalf("tokens = %d, want 280: %#v", summary.Usage.TotalTokens, summary.Usage)
	}
	if summary.Usage.Cost != 0.012 {
		t.Fatalf("cost = %v, want 0.012", summary.Usage.Cost)
	}
	if !summary.AccountingComplete {
		t.Fatal("accounting_complete = false, want true")
	}
	var messages, tools, customs int
	for _, event := range events {
		switch event.Kind {
		case session.KindMessage:
			messages++
		case session.KindToolCall:
			tools++
			payload := event.Payload.(session.ToolCallPayload)
			if payload.Tool != "Bash" || payload.Success == nil || !*payload.Success {
				t.Fatalf("tool payload = %#v", payload)
			}
		case session.KindCustom:
			customs++
		}
	}
	if messages != 1 {
		t.Fatalf("messages = %d, want 1 (subagent text scoped to custom, stream_event dropped)", messages)
	}
	if tools != 1 {
		t.Fatalf("tools = %d, want 1", tools)
	}
	if customs < 2 {
		t.Fatalf("customs = %d, want >=2 (init + subagent)", customs)
	}
}

func TestStreamErrorResultFails(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"22222222-2222-4222-8222-222222222222","model":"sonnet"}
{"type":"result","subtype":"error","is_error":true,"session_id":"22222222-2222-4222-8222-222222222222","result":"Permission denied: Bash"}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	_, err := DecodeStream(context.Background(), strings.NewReader(input), sink)
	if err == nil || !strings.Contains(err.Error(), "error result") {
		t.Fatalf("err = %v, want error result", err)
	}
}

func TestStreamMissingResultFails(t *testing.T) {
	input := `{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	summary, err := DecodeStream(context.Background(), strings.NewReader(input), sink)
	if err == nil || !strings.Contains(err.Error(), "missing terminal result") {
		t.Fatalf("err = %v, want missing result", err)
	}
	if summary.AccountingComplete {
		t.Fatal("accounting_complete = true after missing result, want false")
	}
	if summary.Usage.Cost != 0 {
		t.Fatalf("cost = %v, want 0 (no fabrication)", summary.Usage.Cost)
	}
}

func TestStreamSessionMismatchFails(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","model":"sonnet"}
{"type":"result","subtype":"success","session_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","result":"done","total_cost_usd":0.01}
`
	var events []harness.Event
	sink := func(_ context.Context, event harness.Event) error {
		events = append(events, event)
		return nil
	}
	_, err := DecodeStream(context.Background(), strings.NewReader(input), sink)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("err = %v, want identity changed", err)
	}
}

package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDisplayDerivation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		entry Entry
		want  Display
	}{
		{
			name:  "user message",
			entry: NewMessage(MessagePayload{Role: "user", Text: "\n  hello\nsecond"}),
			want:  Display{Role: "user", Status: "neutral", Title: "User message", Result: "\n  hello\nsecond", Preview: "hello"},
		},
		{
			name:  "assistant error",
			entry: NewMessage(MessagePayload{Role: "assistant", Text: "failed", StopReason: "error"}),
			want:  Display{Role: "agent", Status: "failure", Title: "Agent response", Result: "failed", Preview: "failed"},
		},
		{
			name:  "phase error",
			entry: NewPhaseEnd(PhasePayload{Name: "build", Status: "failed", Error: "compile failed"}),
			want:  Display{Role: "event", Status: "failure", Title: "Attempt finished", Target: "build", Result: "compile failed", Preview: "compile failed"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.entry.Display != test.want {
				t.Fatalf("display = %#v, want %#v", test.entry.Display, test.want)
			}
		})
	}
}

func TestToolDisplayOutcomesAndTarget(t *testing.T) {
	t.Parallel()
	failed := false
	success := true
	tests := []struct {
		name       string
		payload    ToolCallPayload
		wantStatus string
		wantTitle  string
		wantTarget string
	}{
		{name: "success", payload: ToolCallPayload{Tool: "Bash", Arguments: json.RawMessage(`{"command":"go test"}`), Success: &success, Result: "ok"}, wantStatus: "success", wantTitle: "Bash", wantTarget: "go test"},
		{name: "failure", payload: ToolCallPayload{Tool: "Read", Arguments: json.RawMessage(`{"path":"/tmp/file"}`), Success: &failed, Result: "missing"}, wantStatus: "failure", wantTitle: "Read", wantTarget: "/tmp/file"},
		{name: "incomplete", payload: ToolCallPayload{Tool: "Agent", Arguments: json.RawMessage(`{"prompt":"continue"}`), Incomplete: true}, wantStatus: "neutral", wantTitle: "Agent", wantTarget: ""},
		{name: "unknown outcome", payload: ToolCallPayload{Tool: "WebSearch", Arguments: json.RawMessage(`{"query":"Go"}`)}, wantStatus: "neutral", wantTitle: "Web Search", wantTarget: "Go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			display := NewToolCall(test.payload).Display
			if display.Status != test.wantStatus || display.Title != test.wantTitle || display.Target != test.wantTarget {
				t.Fatalf("display = %#v", display)
			}
		})
	}
}

func TestToolNameNormalization(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ input, want string }{
		{"apply_patch", "Edit"}, {"WEB_FETCH", "Web Fetch"}, {"webfetch", "Web Fetch"}, {"WebSearch", "Web Search"}, {"someTool", "Some Tool"},
	} {
		t.Run(test.input, func(t *testing.T) {
			if got := toolTitle(test.input); got != test.want {
				t.Errorf("toolTitle(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestBoundedJSONIsValidAndBounded(t *testing.T) {
	t.Parallel()
	input := map[string]any{"text": strings.Repeat("ж", MaxJSONBytes)}
	data := BoundedJSON(input)
	if len(data) > MaxJSONBytes {
		t.Fatalf("bounded JSON is %d bytes, limit is %d", len(data), MaxJSONBytes)
	}
	if !json.Valid(data) {
		t.Fatalf("bounded JSON is invalid: %s", data)
	}
	var fallback struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(data, &fallback); err != nil {
		t.Fatal(err)
	}
	if !fallback.Truncated {
		t.Fatal("oversized JSON was not marked truncated")
	}
}

func TestTruncatePreservesUTF8AndLimit(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("界", 100)
	got := Truncate(value, 10)
	if len(got) > 10 || !json.Valid(json.RawMessage(`"`+got+`"`)) {
		t.Fatalf("truncate produced invalid or oversized UTF-8: %q (%d bytes)", got, len(got))
	}
}

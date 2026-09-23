package timeline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type fakeReader struct{ entries []harness.NativeEntry }

func (f fakeReader) Entries(context.Context, harness.SessionRef) ([]harness.NativeEntry, error) {
	return f.entries, nil
}

func (f fakeReader) Stats(context.Context, harness.SessionRef) (harness.Stats, error) {
	return harness.Stats{}, nil
}

func (f fakeReader) Report(context.Context, harness.SessionRef, string) (harness.Report, bool, error) {
	return harness.Report{}, false, nil
}

func TestResolveHydratesAgentEventsFromNativeEntries(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := store.Task{ID: "task-1", Request: "r", WorkspacePath: t.TempDir(), State: "building", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = db.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AgentSessions.Reserve(ctx, task.ID, store.AgentSession{StageID: "builder", AgentName: "builder", Role: "builder", Harness: "pi", HarnessSessionID: "sess-1", SessionDirectory: filepath.Join(task.WorkspacePath, "sessions")}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Events.Append(ctx, "", store.Event{ID: "event-1", TaskID: task.ID, PhaseID: "phase-1", Kind: session.KindMessage, NativeEntryID: "a1", RequestID: "req-1", Payload: session.MessagePayload{Role: "assistant", Text: "stale"}, Display: session.Describe(session.KindMessage, session.MessagePayload{Role: "assistant", Text: "stale"}), StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Events.Append(ctx, "", store.Event{ID: "event-2", TaskID: task.ID, PhaseID: "phase-1", Kind: session.KindToolCall, NativeEntryID: "t1", RequestID: "req-1", Payload: session.ToolCallPayload{ToolCallID: "call-1", Tool: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`), Result: "stale"}, Display: session.Describe(session.KindToolCall, session.ToolCallPayload{ToolCallID: "call-1", Tool: "bash"}), StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	events, err := db.Events.List(ctx, task.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	resolver := New(db, fakeReader{entries: []harness.NativeEntry{
		{ID: "a1", Type: "message", Role: "assistant", Text: `{"ok":true}`},
		{ID: "t1", Type: "message", Role: "toolResult", Data: json.RawMessage(`{"role":"toolResult","toolCallId":"call-1","toolName":"bash","isError":false,"content":[{"type":"text","text":"fresh"}]}`)},
	}})
	resolved := resolver.Resolve(ctx, events)
	if len(resolved) != 2 {
		t.Fatalf("resolved = %d events", len(resolved))
	}
	message, _ := resolved[0].Payload.(map[string]any)
	if message["text"] != `{"ok":true}` {
		t.Fatalf("message payload = %#v, want native text", message)
	}
	tool, _ := resolved[1].Payload.(map[string]any)
	if tool["result"] != "fresh" {
		t.Fatalf("tool result = %#v, want native result", tool["result"])
	}
	if tool["tool_call_id"] != "call-1" || tool["arguments"] == nil {
		t.Fatalf("tool payload lost correlation fields: %#v", tool)
	}
}

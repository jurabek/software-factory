package harness

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestRunTurnCorrelatesStreamedEventsToNativeEntries(t *testing.T) {
	agent := &scriptedHarness{
		results: []Result{{Text: `{"ok":true}`, SessionReady: true}},
		events: []session.Entry{
			session.NewMessage(session.MessagePayload{Role: "user", Text: "do it"}),
			session.NewMessage(session.MessagePayload{Role: "assistant", Text: `{"ok":true}`}),
			session.NewToolCall(session.ToolCallPayload{ToolCallID: "call-1", Tool: "bash", Result: "ok"}),
		},
		stats:   Stats{LeafID: "t1"},
		native:  true,
		replies: map[string]string{"req-test": `{"ok":true}`},
		entries: []NativeEntry{
			{ID: "r1", Type: "custom", Data: json.RawMessage(`{"requestId":"req-test"}`)},
			{ID: "u2", ParentID: "r1", Type: "message", Role: "user", Text: "do it"},
			{ID: "a1", ParentID: "u2", Type: "message", Role: "assistant", Text: `{"ok":true}`},
			{ID: "t1", ParentID: "a1", Type: "message", Role: "toolResult", Data: json.RawMessage(`{"role":"toolResult","toolCallId":"call-1","toolName":"bash","isError":false,"content":[{"type":"text","text":"ok"}]}`)},
		},
	}
	deps, db, task := testTurnDeps(t, agent, 0)
	if _, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild); err != nil {
		t.Fatal(err)
	}
	events, err := db.Events.ByRequest(context.Background(), task.ID, "req-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	want := []string{"u2", "a1", "t1"}
	for index, event := range events {
		if event.NativeEntryID != want[index] {
			t.Fatalf("event %d native entry = %q, want %q", index, event.NativeEntryID, want[index])
		}
	}
}

type fakeNativeReader struct {
	stats   Stats
	replies map[string]string
	entries []NativeEntry
}

func (f fakeNativeReader) Entries(context.Context, SessionRef) ([]NativeEntry, error) {
	return f.entries, nil
}

func (f fakeNativeReader) Stats(context.Context, SessionRef) (Stats, error) {
	return f.stats, nil
}

func (f fakeNativeReader) Report(_ context.Context, _ SessionRef, requestID string) (Report, bool, error) {
	text, ok := f.replies[requestID]
	if !ok {
		return Report{}, false, nil
	}
	return Report{EntryID: "native-" + requestID, Text: text}, true, nil
}

func TestReconcilePendingTurnsAdoptsNativeStatsAndRequeuesPhase(t *testing.T) {
	ctx := context.Background()
	_, db, task := testTurnDeps(t, &scriptedHarness{}, 0)
	phase := store.Phase{ID: "phase-1", TaskID: task.ID, Sequence: 1, Name: "builder", Kind: "build", Owner: "builder", Status: "interrupted", Attempt: 1, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := db.Phases.Add(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AgentSessions.Reserve(ctx, task.ID, store.AgentSession{StageID: "builder", AgentName: "builder", Role: "builder", Harness: "pi", HarnessSessionID: "sess-1", SessionDirectory: filepath.Join(task.WorkspacePath, "sessions", "builder", "pi")}); err != nil {
		t.Fatal(err)
	}
	if err := db.AgentSessions.BeginInvocation(ctx, task.ID, "builder", "inv-1", "req-1", phase.ID); err != nil {
		t.Fatal(err)
	}
	reader := fakeNativeReader{stats: Stats{Usage: Usage{Input: 40, Output: 10, TotalTokens: 50, Cost: 0.75}, LeafID: "a1", ContextTokens: 30, ContextWindow: 200}}
	if err := ReconcilePendingTurns(ctx, db, reader); err != nil {
		t.Fatal(err)
	}
	stored, err := db.AgentSessions.Get(ctx, task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cost != 0.75 || stored.LastEntryID != "a1" || stored.PendingInvocationID != "inv-1" {
		t.Fatalf("session = %#v, want adopted stats with preserved pending marker", stored)
	}
	archived, err := db.Phases.ByID(ctx, task.ID, phase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != "queued" {
		t.Fatalf("phase status = %q, want queued for explicit resume", archived.Status)
	}
}

func TestRunTurnReusesRecoveredNativeResponseWithoutPrompting(t *testing.T) {
	agent := &scriptedHarness{results: []Result{{Text: `{"ok":true}`, SessionReady: true}}}
	deps, db, task := testTurnDeps(t, agent, 0)
	ctx := context.Background()
	if _, err := db.AgentSessions.Reserve(ctx, task.ID, store.AgentSession{StageID: "builder", AgentName: "builder", Role: "builder", Harness: "pi", HarnessSessionID: "sess-1", SessionDirectory: filepath.Join(task.WorkspacePath, "sessions", "builder", "pi")}); err != nil {
		t.Fatal(err)
	}
	if err := db.AgentSessions.BeginInvocation(ctx, task.ID, "builder", "inv-1", "req-1", "phase-1"); err != nil {
		t.Fatal(err)
	}
	deps.NativeReader = fakeNativeReader{
		stats:   Stats{Usage: Usage{Input: 12, Output: 3, TotalTokens: 15, Cost: 0.2}, LeafID: "a1"},
		replies: map[string]string{"req-1": `{"ok":true}`},
	}
	result, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild)
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload != `{"ok":true}` {
		t.Fatalf("payload = %q", result.Payload)
	}
	if result.ReportEntryID != "native-req-1" {
		t.Fatalf("report entry = %q, want native-req-1", result.ReportEntryID)
	}
	if len(agent.requests) != 0 {
		t.Fatalf("harness prompted %d times, want reuse without prompting", len(agent.requests))
	}
	stored, err := db.AgentSessions.Get(ctx, task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PendingInvocationID != "" || stored.Cost != 0.2 || stored.LastEntryID != "a1" {
		t.Fatalf("session = %#v, want settled recovered turn", stored)
	}
	envelopes, err := db.Envelopes.List(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 1 || !envelopes[0].Valid || envelopes[0].Payload != `{"ok":true}` {
		t.Fatalf("envelopes = %#v, want one valid recovered envelope", envelopes)
	}
}

func TestRunTurnRedrivesLostTurn(t *testing.T) {
	agent := &scriptedHarness{results: []Result{{Text: `{"ok":true}`, SessionReady: true}}}
	deps, db, task := testTurnDeps(t, agent, 0)
	ctx := context.Background()
	if _, err := db.AgentSessions.Reserve(ctx, task.ID, store.AgentSession{StageID: "builder", AgentName: "builder", Role: "builder", Harness: "pi", HarnessSessionID: "sess-1", SessionDirectory: filepath.Join(task.WorkspacePath, "sessions", "builder", "pi")}); err != nil {
		t.Fatal(err)
	}
	if err := db.AgentSessions.BeginInvocation(ctx, task.ID, "builder", "inv-1", "req-1", "phase-1"); err != nil {
		t.Fatal(err)
	}
	deps.NativeReader = fakeNativeReader{replies: map[string]string{}}
	if _, err := runTurn(t, deps, db, task, store.Phase{ID: "phase-1"}, "builder", validBuild); err != nil {
		t.Fatal(err)
	}
	if len(agent.requests) != 1 {
		t.Fatalf("harness prompts = %d, want lost turn re-driven once", len(agent.requests))
	}
	stored, err := db.AgentSessions.Get(ctx, task.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PendingInvocationID != "" {
		t.Fatalf("session = %#v, want pending marker cleared after re-drive", stored)
	}
}

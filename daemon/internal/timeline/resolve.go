// Package timeline resolves agent-derived timeline events from the
// authoritative native harness session. The factory store keeps ordering,
// ownership, and native entry references; payloads are hydrated here on read.
package timeline

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Resolver hydrates agent-derived events from native session entries.
type Resolver struct {
	DB     *store.Store
	Reader harness.NativeReader
}

// New constructs a timeline resolver.
func New(db *store.Store, reader harness.NativeReader) *Resolver {
	return &Resolver{DB: db, Reader: reader}
}

// Resolve returns the events with agent-derived payloads resolved from their
// referenced native entries. Events without a resolvable reference pass
// through unchanged, so factory-generated events keep their own content.
func (r *Resolver) Resolve(ctx context.Context, events []store.Event) []store.Event {
	if r == nil || r.Reader == nil || len(events) == 0 {
		return events
	}
	tasks := map[string]bool{}
	for _, event := range events {
		if event.NativeEntryID != "" {
			tasks[event.TaskID] = true
		}
	}
	if len(tasks) == 0 {
		return events
	}
	index := map[string]harness.NativeEntry{}
	for taskID := range tasks {
		sessions, err := r.DB.AgentSessions.List(ctx, taskID)
		if err != nil {
			continue
		}
		for _, agentSession := range sessions {
			if agentSession.HarnessSessionID == "" || agentSession.SessionDirectory == "" {
				continue
			}
			entries, err := r.Reader.Entries(ctx, harness.SessionRef{ID: agentSession.HarnessSessionID, Directory: agentSession.SessionDirectory})
			if err != nil {
				continue
			}
			for _, entry := range entries {
				index[entry.ID] = entry
			}
		}
	}
	if len(index) == 0 {
		return events
	}
	resolved := make([]store.Event, len(events))
	copy(resolved, events)
	for i := range resolved {
		if resolved[i].NativeEntryID == "" {
			continue
		}
		entry, ok := index[resolved[i].NativeEntryID]
		if !ok {
			continue
		}
		hydrate(&resolved[i], entry)
	}
	return resolved
}

func hydrate(event *store.Event, entry harness.NativeEntry) {
	switch event.Kind {
	case session.KindMessage:
		values, ok := event.Payload.(map[string]any)
		if !ok {
			return
		}
		if entry.Role != "" {
			values["role"] = strings.ToLower(entry.Role)
		}
		if entry.Text != "" {
			values["text"] = entry.Text
		}
		if payload, err := remarshal[session.MessagePayload](values); err == nil {
			event.Display = session.Describe(session.KindMessage, payload)
		}
	case session.KindToolCall:
		values, ok := event.Payload.(map[string]any)
		if !ok {
			return
		}
		var data struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			IsError    bool            `json:"isError"`
			Content    json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(entry.Data, &data)
		if data.ToolCallID != "" {
			values["tool_call_id"] = data.ToolCallID
		}
		if data.ToolName != "" {
			values["tool"] = data.ToolName
		}
		if text := contentText(data.Content); text != "" {
			values["result"] = text
		}
		values["success"] = !data.IsError
		delete(values, "incomplete")
		if payload, err := remarshal[session.ToolCallPayload](values); err == nil {
			event.Display = session.Describe(session.KindToolCall, payload)
		}
	}
}

func remarshal[T any](values map[string]any) (T, error) {
	var out T
	body, err := json.Marshal(values)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var builder strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return builder.String()
	}
	return ""
}

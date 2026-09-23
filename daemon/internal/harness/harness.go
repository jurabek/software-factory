package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

// ErrNoNativeRecord signals that a session has no native journal to reconcile
// from, so the adapter's streamed result stays authoritative.
var ErrNoNativeRecord = errors.New("no native session record")

type Model struct {
	Provider, ID  string
	ContextWindow int
	Thinking      []string
}

// SessionSpec describes the native session to open. One session maps to a Task
// plus stage and is reused across correction turns, message continuations, and
// Attempts. The session persists on disk, so Open resumes an existing session
// and creates one when it is missing.
type SessionSpec struct {
	CWD                   string
	Model                 string
	Thinking              string
	SystemPrompt          string
	SessionID             string
	SessionDirectory      string
	AdditionalDirectories []string
}

// Prompt is one user message sent to an open session. ForkAtEntryID, when set,
// branches the native session at the given entry before sending the message so
// exact retry rejoins the attempt's recorded input checkpoint.
type Prompt struct {
	RequestID     string
	Attempt       int
	Text          string
	ForkAtEntryID string
	DeadlineMS    int
}

type Event = session.Entry

type (
	EventSink func(context.Context, Event) error

	Cost struct {
		Input,
		Output,
		CacheRead,
		CacheWrite,
		Reasoning,
		Total float64
	}

	Usage struct {
		Input,
		Output,
		CacheRead,
		CacheWrite,
		Reasoning,
		TotalTokens int
		Cost  float64
		Costs Cost
	}
)

type Result struct {
	Text                         string
	ExitCode                     int
	SessionID, Provider, Model   string
	Usage                        Usage
	ContextTokens, ContextWindow int
	SessionReady                 bool
	NativeTranscriptPath         string
	// LeafEntryID is the native session cursor after the turn settles.
	LeafEntryID string
	// ReportText is the full assistant text authored for the request, resolved
	// from its native entry. ReportEntryID is that entry's stable id.
	ReportText    string
	ReportEntryID string
}

// TurnResult is one settled turn's payload plus the native reference of the
// report it published.
type TurnResult struct {
	Payload       string
	ReportText    string
	ReportEntryID string
}

// SessionRef identifies one native harness session on disk.
type SessionRef struct {
	ID        string
	Directory string
}

// NativeEntry is a single authoritative harness session entry.
type NativeEntry struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Text      string          `json:"text,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Report is an agent-authored report resolved from its native assistant entry.
type Report struct {
	EntryID string
	Text    string
}

// Stats are native session totals: usage and cost across every entry,
// including tool work and compaction.
type Stats struct {
	Usage         Usage
	ContextTokens int
	ContextWindow int
	LeafID        string
}

// NativeReader reads the authoritative harness session record without a live
// process. It backs startup reconciliation and timeline hydration.
type NativeReader interface {
	Entries(ctx context.Context, ref SessionRef) ([]NativeEntry, error)
	Stats(ctx context.Context, ref SessionRef) (Stats, error)
	// Report returns the assistant text belonging to the request's native
	// subtree, resolved by the factory request id.
	Report(ctx context.Context, ref SessionRef, requestID string) (Report, bool, error)
}

// Session is an open native session. Prompt streams events and settles once the
// agent has fully stopped; Close releases the underlying process.
type Session interface {
	Prompt(ctx context.Context, prompt Prompt, sink EventSink) (Result, error)
	Stats(ctx context.Context) (Stats, error)
	Entries(ctx context.Context) ([]NativeEntry, error)
	Report(ctx context.Context, requestID string) (Report, bool, error)
	Close() error
}

// Harness opens session-oriented native agents.
type Harness interface {
	Models(context.Context) ([]Model, error)
	Open(context.Context, SessionSpec) (Session, error)
}
type Registry map[string]Harness

func (r Registry) Get(name string) (Harness, bool) { h, ok := r[name]; return h, ok }

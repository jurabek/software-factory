package store

import (
	"github.com/jurabek/software-factory/daemon/internal/session"
)

type AgentSession struct {
	TaskID               string        `json:"task_id"`
	StageID              string        `json:"stage_id"`
	AgentName            string        `json:"agent_name,omitempty"`
	Role                 string        `json:"role"`
	Harness              string        `json:"harness"`
	Provider             string        `json:"provider,omitempty"`
	Model                string        `json:"model,omitempty"`
	Thinking             string        `json:"thinking,omitempty"`
	Color                string        `json:"color,omitempty"`
	HarnessSessionID     string        `json:"harness_session_id"`
	SessionDirectory     string        `json:"session_directory"`
	SessionReady         bool          `json:"session_ready"`
	NativeTranscriptPath string        `json:"native_transcript_path,omitempty"`
	PendingInvocationID  string        `json:"-"`
	PendingRequestID     string        `json:"-"`
	PendingPhaseID       string        `json:"-"`
	ContextTokens        int           `json:"context_tokens,omitempty"`
	ContextWindow        int           `json:"context_window,omitempty"`
	Usage                session.Usage `json:"usage"`
	Cost                 float64       `json:"cost"`
	LastEntryID          string        `json:"last_entry_id,omitempty"`
	CreatedAt            string        `json:"created_at"`
	LastUsedAt           string        `json:"last_used_at"`
}

// PendingAgentSessions lists sessions with an in-flight invocation. The
// reconcile loop resolves each against the native session before settling it.

// ReconcileAgentStats records native-derived usage, cost, context, and leaf for
// an in-flight session without clearing its pending marker. The cost is
// absolute, so re-driving a lost turn stays idempotent.

// ClearAgentInvocation releases a pending marker that no longer maps to a
// usable native turn, leaving the session settled and ready to re-drive.

// RequeueInterruptedPhase returns an in-flight phase to the queue so an
// explicit resume reuses it instead of creating a replacement attempt.

// ResetAgentSession starts a fresh native session for a stage, discarding the
// prior conversation. An exact retry uses it when the source attempt had no
// recorded native checkpoint to fork from. A pending invocation blocks it.

package store

type Phase struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	StageID        string `json:"stage_id,omitempty"`
	Kind           string `json:"kind"`
	Owner          string `json:"owner"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Sequence       int    `json:"sequence"`
	Attempt        int    `json:"attempt"`
	Retries        int    `json:"retries"`
	BranchID       string `json:"branch_id,omitempty"`
	DefinitionID   string `json:"definition_id,omitempty"`
	DefinitionRev  int    `json:"definition_revision,omitempty"`
	InputSnapshot  string `json:"input_snapshot,omitempty"`
	OutputSnapshot string `json:"output_snapshot,omitempty"`
	Superseded     bool   `json:"superseded,omitempty"`
	// NativeBaseEntryID is the native session leaf immediately before this
	// attempt's first invocation. It is the checkpoint an exact retry forks at.
	NativeBaseEntryID string `json:"native_base_entry_id,omitempty"`
	// ForkNative records that this attempt must branch the native session at
	// NativeBaseEntryID instead of continuing the current leaf.
	ForkNative bool   `json:"fork_native,omitempty"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
}

// StartPhaseWithEvent creates a running phase, updates its Task's active phase
// and branch head, and records the phase-start event atomically.

// EndPhaseWithEvent publishes a phase result and its lifecycle event
// atomically. Task progression, when needed, uses the transition variant.

// CompletePhaseWithTransitionAndEvent publishes a phase result, its Task
// transition, and the corresponding lifecycle event atomically. The event
// trace is derived output and is written only after the database commit.

// CompletePlannerPhaseWithApproval publishes a planner result and approval
// candidate together with the phase transition and event.

// CompleteVerificationPhaseWithEvidenceAndEvent publishes verification
// evidence, the phase result, the Task transition, and the lifecycle event in
// one transaction. Individual check observations may already be durable; the
// final publication is the authoritative successful verification boundary.

func nullIfTerminalState(state string, ended string) any {
	if state == "completed" || state == "blocked" || state == "aborted" {
		return ended
	}
	return nil
}

// SetPhaseNativeBase records the native session checkpoint an attempt started
// from. It only fills an empty checkpoint so a forked retry keeps the source
// attempt's recorded boundary.

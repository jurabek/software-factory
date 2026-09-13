// Package intervention owns operator intent against a task: anchor validation,
// target resolution, intent policy, and the durable branch/attempt/definition
// materialization for retry, revise, and repair. The orchestrator only launches
// the workflow afterward.
package intervention

import (
	"errors"
	"path/filepath"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// ErrInvalidFeedback reports a missing message for an intent that requires one.
var ErrInvalidFeedback = errors.New("feedback is required")

// Anchor is a canonical artifact coordinate. Rendered DOM paths and pixel
// positions are never persisted.
type Anchor struct {
	Kind      string `json:"kind"`
	Start     *int   `json:"start,omitempty"`
	End       *int   `json:"end,omitempty"`
	Quote     string `json:"quote,omitempty"`
	Pointer   string `json:"pointer,omitempty"`
	ValueHash string `json:"value_digest,omitempty"`
	Block     string `json:"block,omitempty"`
}

// Target accepts exactly one of event, artifact, or attempt.
type Target struct {
	EventID    string  `json:"event_id,omitempty"`
	ArtifactID string  `json:"artifact_id,omitempty"`
	AttemptID  string  `json:"attempt_id,omitempty"`
	Anchor     *Anchor `json:"anchor,omitempty"`
}

// Request is the control-plane intervention request.
type Request struct {
	Target             Target `json:"target"`
	Intent             string `json:"intent"`
	Message            string `json:"message"`
	ExpectedBranchHead string `json:"expected_branch_head,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
	Delivery           string `json:"delivery,omitempty"`
}

// RetryRequest identifies a retry by idempotency key.
type RetryRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// Deps are the collaborators an intervention service needs.
type Deps struct {
	Store     *store.DB
	Git       factorygit.Runner
	Snapshots *workspace.Service
	Root      string
}

// Service applies operator interventions to tasks.
type Service struct {
	deps Deps
}

// New constructs an intervention service.
func New(deps Deps) *Service {
	return &Service{deps: deps}
}

func (s *Service) taskDir(id string) string {
	return filepath.Join(s.deps.Root, "tasks", id)
}

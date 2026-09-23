package stagekit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// RandomID returns a fresh opaque identifier.
func RandomID() string { return uuid.NewV7().String() }

func trimSpace(value string) string { return strings.TrimSpace(value) }

// PhaseByID loads one attempt.

// BeginPhase starts a fresh attempt and emits its start event.

// BeginOrReusePhase starts a stage phase, reusing a queued or running retry
// attempt for the same stage when one exists.

// EndPhase finishes a phase without a transition, capturing its output snapshot.

// Fail marks a phase failed without a transition.

// Completion describes an atomic phase completion and optional transition.
type Completion struct {
	Phase       store.Phase
	From, To    State
	Status      string
	Cause       error
	Approval    string
	Checks      []store.Check
	Comparisons []store.Comparison
	Planner     bool
}

// Complete atomically persists evidence, completion, and transition.

// LatestStageAttempt returns the newest non-superseded attempt for a stage.

// SuccessfulPhase returns the latest successful attempt for a stage.

// RequireAttempt verifies an attempt is still the successful one for a stage.

// AttemptAfter reports whether an attempt was produced after its upstream.

// PhaseEnvelope returns the latest valid envelope for a phase.

// PlanApprovalDigest binds approval to the exact plan payload.
func PlanApprovalDigest(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// EligibleAfter owns downstream lineage: a result is reusable only when it was
// produced after its upstream result.
func EligibleAfter(attemptSequence, upstreamSequence int) bool {
	return attemptSequence > upstreamSequence
}

// PlanDigest binds a phase key and revision to an amendment. Control-plane
// retry/revise operations reuse it so definition digests stay consistent.
func PlanDigest(key string, revision int, amendment string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", key, revision, amendment)))
	return fmt.Sprintf("%x", sum)
}

package stagekit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// RandomID returns a fresh opaque identifier.
func RandomID() string { return uuid.NewV7().String() }

// Completion describes an atomic phase completion and optional transition.
type Completion struct {
	Phase       store.Phase
	From, To    string
	Status      string
	Cause       error
	Approval    string
	Checks      []store.Check
	Comparisons []store.Comparison
	Planner     bool
}

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

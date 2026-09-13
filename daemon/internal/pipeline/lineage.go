// Package pipeline owns the factory's fixed workflow and typed stage handoffs.
//
// Lineage rules: a downstream saved result is eligible for reuse only when it
// was produced after its upstream result. A changed plan (new attempt)
// invalidates builds, verifications, and reviews produced earlier; a changed
// build invalidates verifications and reviews. Verify-only retry reuses the
// selected plan/build because their attempt identities are unchanged.
package pipeline

// EligibleAfter reports whether a downstream attempt produced at
// downstreamSeq may be reused for an upstream attempt produced at
// upstreamSeq. Sequences come from durable phase ordering.
func EligibleAfter(downstreamSeq, upstreamSeq int) bool {
	return downstreamSeq > upstreamSeq
}

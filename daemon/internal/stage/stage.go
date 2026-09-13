// Package stage holds the leaf value types shared by the workflow stages and
// the pipeline that orders them. It imports no store, git, or workspace code so
// both sides can depend on it without creating a cycle.
package stage

// Input carries the execution identity a stage needs to resolve its durable
// state. It never contains the orchestrator or a dependency bag.
type Input struct {
	TaskID string
}

// PlanResult is the planner's typed handoff.
type PlanResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
	Approved   bool
}

// BuildResult is the builder's typed handoff.
type BuildResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
}

// VerificationResult is the verifier's typed handoff.
type VerificationResult struct {
	AttemptID  string
	SnapshotID string
	Report     string
	Passed     bool
}

// ReviewResult is the reviewer's typed handoff.
type ReviewResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
	Approved   bool
}

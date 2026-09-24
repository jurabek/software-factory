// Package stagekit holds lifecycle machinery shared by stage modules. It is a
// stage-facing support library: stages use it to begin attempts, apply task
// transitions, publish results, drain messages, and resolve resume state.
// It never orchestrates stages and is never used to implement stage logic.
package stagekit

const (
	Preparing        = "preparing"
	Planning         = "planning"
	AwaitingApproval = "awaiting_plan_approval"
	Building         = "building"
	Checking         = "checking"
	Reviewing        = "reviewing"
	Completed        = "completed"
	Blocked          = "blocked"
	Paused           = "paused"
	Aborted          = "aborted"
)

var transitions = map[string]map[string]bool{
	Preparing:        {Planning: true, Building: true, Blocked: true, Paused: true, Aborted: true},
	Planning:         {Planning: true, AwaitingApproval: true, Building: true, Blocked: true, Paused: true, Aborted: true},
	AwaitingApproval: {Planning: true, Building: true, Blocked: true, Aborted: true},
	Building:         {Checking: true, Blocked: true, Paused: true, Aborted: true},
	Checking:         {Reviewing: true, Blocked: true, Paused: true, Aborted: true},
	Reviewing:        {Completed: true, Blocked: true, Paused: true, Aborted: true},
	Paused:           {Preparing: true, Planning: true, Building: true, Checking: true, Reviewing: true, Blocked: true, Aborted: true},
	Blocked:          {Preparing: true, Planning: true, Building: true, Checking: true, Reviewing: true, Aborted: true},
}

// CanTransition reports whether from may legally advance to to.
func CanTransition(from, to string) bool { return transitions[from][to] }

// IsActive reports whether state is an in-flight task state.
func IsActive(state string) bool {
	switch state {
	case Preparing, Planning, AwaitingApproval, Building, Checking, Reviewing:
		return true
	}
	return false
}

// StateForRole maps a stage role to the state a reopened task resumes in.
// Control-plane operations (reopen on message) use it to seed a stage that
// then owns its own transition.
func StateForRole(role string) string {
	switch role {
	case "planner":
		return Planning
	case "reviewer":
		return Reviewing
	default:
		return Building
	}
}

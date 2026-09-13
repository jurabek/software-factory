// Package stagekit holds lifecycle machinery shared by stage modules. It is a
// stage-facing support library: stages use it to begin attempts, apply task
// transitions, publish results, drain messages, and resolve resume state.
// It never orchestrates stages and is never used to implement stage logic.
package stagekit

import (
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// State is a task lifecycle state. Stage modules apply transitions through Kit;
// the orchestrator never sets state.
type State string

const (
	Preparing        State = "preparing"
	Planning         State = "planning"
	AwaitingApproval State = "awaiting_plan_approval"
	Building         State = "building"
	Checking         State = "checking"
	Reviewing        State = "reviewing"
	Completed        State = "completed"
	Blocked          State = "blocked"
	Paused           State = "paused"
	Aborted          State = "aborted"
)

var transitions = map[State]map[State]bool{
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
func CanTransition(from, to State) bool { return transitions[from][to] }

// IsActive reports whether state is an in-flight task state.
func IsActive(state State) bool {
	switch state {
	case Preparing, Planning, AwaitingApproval, Building, Checking, Reviewing:
		return true
	}
	return false
}

// CheckTransition returns an error for an illegal task transition.
func CheckTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid task transition %q to %q", from, to)
	}
	return nil
}

// StateForRole maps a stage role to the state a reopened task resumes in.
// Control-plane operations (reopen on message) use it to seed a stage that
// then owns its own transition.
func StateForRole(role string) State {
	switch role {
	case "planner":
		return Planning
	case "reviewer":
		return Reviewing
	default:
		return Building
	}
}

// StateForPhase maps an attempt to the state a retried task resumes in so the
// owning stage can begin from a legal transition origin.
func StateForPhase(phase store.Phase) State {
	switch phase.Name {
	case "planning":
		return Planning
	case "checks":
		return Checking
	case "reviewing":
		return Reviewing
	case "building":
		return Building
	default:
		return StateForRole(phase.Owner)
	}
}

package factory

import "fmt"

type State string

const (
	Draft            State = "draft"
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

var transitions = map[State]map[State]bool{Draft: {Preparing: true, Aborted: true}, Preparing: {Planning: true, Blocked: true, Paused: true, Aborted: true}, Planning: {AwaitingApproval: true, Blocked: true, Paused: true, Aborted: true}, AwaitingApproval: {Building: true, Blocked: true, Aborted: true}, Building: {Checking: true, Blocked: true, Paused: true, Aborted: true}, Checking: {Reviewing: true, Blocked: true, Paused: true, Aborted: true}, Reviewing: {Completed: true, Blocked: true, Paused: true, Aborted: true}, Paused: {Preparing: true, Planning: true, Building: true, Checking: true, Reviewing: true, Blocked: true, Aborted: true}, Blocked: {Preparing: true, Planning: true, Building: true, Checking: true, Reviewing: true, Aborted: true}}

func CanTransition(from, to State) bool { return transitions[from][to] }
func Transition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid campaign transition %q to %q", from, to)
	}
	return nil
}

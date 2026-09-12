package factory

import (
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// AvailableActions returns server-computed actions for an attempt.
// The UI renders these without duplicating transition policy.
func AvailableActions(phase *store.Phase, taskState string) []string {
	actions := make([]string, 0, 3)
	switch taskState {
	case string(AwaitingApproval):
		actions = append(actions, "approve", "abort")
	case string(Paused):
		actions = append(actions, "resume", "abort")
	case string(Blocked):
		actions = append(actions, "resume", "abort")
	case string(Aborted):
	case string(Completed):
	case string(Preparing), string(Planning), string(Building), string(Checking), string(Reviewing):
		actions = append(actions, "pause", "abort")
	}
	if phase != nil && phase.Status != "running" && phase.Status != "queued" {
		actions = append(actions, "retry")
	}
	return actions
}

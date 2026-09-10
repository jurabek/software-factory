package factory

import (
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// AvailableActions returns server-computed actions for an attempt.
// The UI renders these without duplicating transition policy.
func AvailableActions(phase *store.Phase, taskState string) []string {
	actions := make([]string, 0, 3)
	switch taskState {
	case string(Draft):
		actions = append(actions, "start", "abort")
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

// ValidateIntent enforces the atomic intervention policy before any write.
func ValidateIntent(intent string, phase *store.Phase, taskState string, message string) error {
	switch intent {
	case "comment":
		return nil
	case "steer", "follow_up":
		if phase == nil || phase.Status != "running" || phase.Kind != "agent" {
			return store.ErrConflict
		}
		if message == "" {
			return ErrInvalidFeedback
		}
		return nil
	case "retry":
		if phase == nil {
			return store.ErrConflict
		}
		if phase.Status == "running" || phase.Status == "queued" {
			return store.ErrConflict
		}
		return nil
	case "revise":
		if phase == nil {
			return store.ErrConflict
		}
		if phase.Status == "running" || phase.Status == "queued" {
			return store.ErrConflict
		}
		if message == "" {
			return ErrInvalidFeedback
		}
		return nil
	case "repair":
		if phase == nil {
			if taskState != string(Completed) && taskState != string(Blocked) {
				return store.ErrConflict
			}
			if message == "" {
				return ErrInvalidFeedback
			}
			return nil
		}
		if phase.Status == "running" || phase.Status == "queued" {
			return store.ErrConflict
		}
		if message == "" {
			return ErrInvalidFeedback
		}
		return nil
	default:
		return store.ErrConflict
	}
}

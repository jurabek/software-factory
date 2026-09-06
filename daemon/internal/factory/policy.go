package factory

import (
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// AvailableActions returns server-computed actions for an attempt.
// The UI renders these without duplicating transition policy.
func AvailableActions(phase *store.Phase, taskState string) []string {
	if phase == nil {
		switch taskState {
		case string(Draft):
			return []string{"comment", "start"}
		case string(Completed):
			return []string{"comment", "retry", "revise", "repair"}
		default:
			return []string{"comment"}
		}
	}
	if phase.Superseded {
		return []string{"comment"}
	}
	switch phase.Status {
	case "running":
		if phase.Kind == "agent" {
			return []string{"comment", "steer", "follow_up", "pause", "abort"}
		}
		return []string{"comment", "pause", "abort"}
	case "failed", "interrupted":
		if phase.Kind == "check" {
			return []string{"comment", "retry", "repair"}
		}
		return []string{"comment", "retry", "revise", "repair"}
	case "success":
		if taskState == string(Completed) {
			return []string{"comment", "retry", "revise", "repair"}
		}
		return []string{"comment", "retry", "revise"}
	case "queued":
		return []string{"comment", "pause", "abort"}
	default:
		return []string{"comment"}
	}
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

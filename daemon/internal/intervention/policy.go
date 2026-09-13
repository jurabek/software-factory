package intervention

import (
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

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
			if taskState != string(stagekit.Completed) && taskState != string(stagekit.Blocked) {
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

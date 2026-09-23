package messaging

import (
	"context"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Target accepts exactly one of event or attempt.
type Target struct {
	EventID   string `json:"event_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
}

// Resolve maps a target to its storage coordinates and owning attempt. It
// accepts at most one of event or attempt; an empty target resolves to the
// latest attempt for the task.
func (s *Service) Resolve(ctx context.Context, taskID string, target Target) (string, string, *store.Phase, error) {
	count := 0
	if target.EventID != "" {
		count++
	}
	if target.AttemptID != "" {
		count++
	}
	if count > 1 {
		return "", "", nil, fmt.Errorf("target accepts exactly one of event_id or attempt_id")
	}
	if target.AttemptID != "" {
		phase, err := s.deps.Store.PhaseByID(ctx, taskID, target.AttemptID)
		if err != nil {
			return "", "", nil, err
		}
		return "attempt", phase.ID, &phase, nil
	}
	if target.EventID != "" {
		event, err := s.deps.Store.EventByID(ctx, taskID, target.EventID)
		if err != nil {
			return "", "", nil, err
		}
		attemptID := event.AttemptID
		if attemptID == "" {
			attemptID = event.PhaseID
		}
		if attemptID == "" {
			return "event", event.ID, nil, nil
		}
		phase, err := s.deps.Store.PhaseByID(ctx, taskID, attemptID)
		if err != nil {
			return "event", event.ID, nil, nil
		}
		return "event", event.ID, &phase, nil
	}
	phases, err := s.deps.Store.Phases(ctx, taskID)
	if err != nil {
		return "", "", nil, err
	}
	if len(phases) == 0 {
		return "task", taskID, nil, nil
	}
	latest := phases[len(phases)-1]
	return "task", taskID, &latest, nil
}

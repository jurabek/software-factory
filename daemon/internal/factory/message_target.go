package factory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Anchor is a canonical artifact coordinate used by Messages.
type Anchor struct {
	Kind      string `json:"kind"`
	Start     *int   `json:"start,omitempty"`
	End       *int   `json:"end,omitempty"`
	Quote     string `json:"quote,omitempty"`
	Pointer   string `json:"pointer,omitempty"`
	ValueHash string `json:"value_digest,omitempty"`
	Block     string `json:"block,omitempty"`
}

func phaseID(phase *store.Phase) string {
	if phase == nil {
		return ""
	}
	return phase.ID
}

func branchID(phase *store.Phase) string {
	if phase == nil {
		return ""
	}
	return phase.BranchID
}

func (s *Service) resolveTarget(ctx context.Context, taskID string, target MessageTarget) (string, string, *store.Phase, error) {
	count := 0
	if target.EventID != "" {
		count++
	}
	if target.ArtifactID != "" {
		count++
	}
	if target.AttemptID != "" {
		count++
	}
	if count > 1 {
		return "", "", nil, fmt.Errorf("target accepts exactly one of event_id, artifact_id, or attempt_id")
	}
	if target.AttemptID != "" {
		phase, err := s.db.PhaseByID(ctx, taskID, target.AttemptID)
		if err != nil {
			return "", "", nil, err
		}
		return "attempt", phase.ID, &phase, nil
	}
	if target.EventID != "" {
		event, err := s.db.EventByID(ctx, taskID, target.EventID)
		if err != nil {
			return "", "", nil, err
		}
		attemptID := event.AttemptID
		if attemptID == "" {
			attemptID = event.PhaseID
		}
		if attemptID == "" && event.ArtifactID != "" {
			artifact, artifactErr := s.db.Artifact(ctx, taskID, event.ArtifactID)
			if artifactErr != nil {
				return "", "", nil, artifactErr
			}
			attemptID = artifact.AttemptID
		}
		if attemptID == "" {
			return "event", event.ID, nil, nil
		}
		phase, err := s.db.PhaseByID(ctx, taskID, attemptID)
		if err != nil {
			return "event", event.ID, nil, nil
		}
		return "event", event.ID, &phase, nil
	}
	if target.ArtifactID != "" {
		artifact, err := s.db.Artifact(ctx, taskID, target.ArtifactID)
		if err != nil {
			return "", "", nil, err
		}
		if artifact.AttemptID == "" {
			return "artifact", artifact.ID, nil, nil
		}
		phase, err := s.db.PhaseByID(ctx, taskID, artifact.AttemptID)
		if err != nil {
			return "artifact", artifact.ID, nil, nil
		}
		return "artifact", artifact.ID, &phase, nil
	}
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return "", "", nil, err
	}
	if len(phases) == 0 {
		return "task", taskID, nil, nil
	}
	latest := phases[len(phases)-1]
	return "task", taskID, &latest, nil
}

func (s *Service) validateAnchor(ctx context.Context, taskID string, target MessageTarget) error {
	if target.ArtifactID == "" || target.Anchor == nil {
		return nil
	}
	artifact, err := s.db.Artifact(ctx, taskID, target.ArtifactID)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(artifact.Path)
	if err != nil {
		return store.ErrStaleAnchor
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(body))
	if artifact.Digest != "" && actual != artifact.Digest {
		return store.ErrStaleAnchor
	}
	anchor := target.Anchor
	switch anchor.Kind {
	case "text_range", "line_range", "block":
		if anchor.Quote != "" && !strings.Contains(string(body), anchor.Quote) {
			return store.ErrStaleAnchor
		}
		if anchor.Start != nil && anchor.End != nil {
			if *anchor.Start < 0 || *anchor.End > len(body) || *anchor.Start > *anchor.End {
				return store.ErrStaleAnchor
			}
			if anchor.Quote != "" && string(body[*anchor.Start:*anchor.End]) != anchor.Quote && !strings.Contains(string(body), anchor.Quote) {
				return store.ErrStaleAnchor
			}
		}
	case "json_pointer":
		if anchor.ValueHash != "" {
			var payload any
			if json.Unmarshal(body, &payload) != nil {
				return store.ErrStaleAnchor
			}
		}
	default:
		if anchor.Kind == "" {
			return fmt.Errorf("anchor kind is required")
		}
		return fmt.Errorf("unknown anchor kind %q", anchor.Kind)
	}
	return nil
}

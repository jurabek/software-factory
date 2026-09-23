package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Anchor is a canonical artifact coordinate. Rendered DOM paths and pixel
// positions are never persisted.
type Anchor struct {
	Kind      string `json:"kind"`
	Start     *int   `json:"start,omitempty"`
	End       *int   `json:"end,omitempty"`
	Quote     string `json:"quote,omitempty"`
	Pointer   string `json:"pointer,omitempty"`
	ValueHash string `json:"value_digest,omitempty"`
	Block     string `json:"block,omitempty"`
}

// Target accepts exactly one of event, artifact, or attempt.
type Target struct {
	EventID    string  `json:"event_id,omitempty"`
	ArtifactID string  `json:"artifact_id,omitempty"`
	AttemptID  string  `json:"attempt_id,omitempty"`
	Anchor     *Anchor `json:"anchor,omitempty"`
}

// Resolve maps a target to its storage coordinates and owning attempt. It
// accepts at most one of event, artifact, or attempt; an empty target resolves
// to the latest attempt for the task.
func (s *Service) Resolve(ctx context.Context, taskID string, target Target) (string, string, *store.Phase, error) {
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
		if attemptID == "" && event.ArtifactID != "" {
			artifact, artifactErr := s.deps.Store.Artifact(ctx, taskID, event.ArtifactID)
			if artifactErr != nil {
				return "", "", nil, artifactErr
			}
			attemptID = artifact.AttemptID
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
	if target.ArtifactID != "" {
		artifact, err := s.deps.Store.Artifact(ctx, taskID, target.ArtifactID)
		if err != nil {
			return "", "", nil, err
		}
		if artifact.AttemptID == "" {
			return "artifact", artifact.ID, nil, nil
		}
		phase, err := s.deps.Store.PhaseByID(ctx, taskID, artifact.AttemptID)
		if err != nil {
			return "artifact", artifact.ID, nil, nil
		}
		return "artifact", artifact.ID, &phase, nil
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

// ValidateAnchor checks that an anchor still matches the artifact it points at.
func (s *Service) ValidateAnchor(ctx context.Context, taskID string, target Target) error {
	if target.ArtifactID == "" || target.Anchor == nil {
		return nil
	}
	artifact, err := s.deps.Store.Artifact(ctx, taskID, target.ArtifactID)
	if err != nil {
		return err
	}
	body := []byte(artifact.Content)
	if artifact.Path != "" {
		body, err = os.ReadFile(artifact.Path)
		if err != nil {
			return store.ErrStaleAnchor
		}
	}
	actual := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if artifact.Digest != "" && actual != artifact.Digest {
		return store.ErrStaleAnchor
	}
	anchor := target.Anchor
	switch anchor.Kind {
	case "text_range", "line_range", "block":
		if anchor.Start != nil && anchor.End != nil {
			if *anchor.Start < 0 || *anchor.End > len(body) || *anchor.Start > *anchor.End {
				return store.ErrStaleAnchor
			}
			if anchor.Quote != "" && string(body[*anchor.Start:*anchor.End]) != anchor.Quote {
				return store.ErrStaleAnchor
			}
		} else if anchor.Quote != "" && !strings.Contains(string(body), anchor.Quote) {
			return store.ErrStaleAnchor
		}
	case "json_pointer":
		value, pointerErr := resolveJSONPointer(body, anchor.Pointer)
		if pointerErr != nil {
			return store.ErrStaleAnchor
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return store.ErrStaleAnchor
		}
		valueDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
		if anchor.ValueHash != "" && anchor.ValueHash != valueDigest {
			return store.ErrStaleAnchor
		}
	default:
		if anchor.Kind == "" {
			return fmt.Errorf("anchor kind is required")
		}
		return fmt.Errorf("unknown anchor kind %q", anchor.Kind)
	}
	return nil
}

func resolveJSONPointer(body []byte, pointer string) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer")
	}
	for token := range strings.SplitSeq(pointer[1:], "/") {
		decoded, err := decodeJSONPointerToken(token)
		if err != nil {
			return nil, err
		}
		token = decoded
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[token]
			if !ok {
				return nil, fmt.Errorf("JSON pointer member not found")
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(current) || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("JSON pointer index not found")
			}
			value = current[index]
		default:
			return nil, fmt.Errorf("JSON pointer cannot descend into value")
		}
	}
	return value, nil
}

func decodeJSONPointerToken(token string) (string, error) {
	var decoded strings.Builder
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			decoded.WriteByte(token[index])
			continue
		}
		if index+1 >= len(token) || (token[index+1] != '0' && token[index+1] != '1') {
			return "", fmt.Errorf("invalid JSON pointer escape")
		}
		index++
		if token[index] == '0' {
			decoded.WriteByte('~')
		} else {
			decoded.WriteByte('/')
		}
	}
	return decoded.String(), nil
}

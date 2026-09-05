package factory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jurabek/software-factory/internal/store"
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

// InterventionTarget accepts exactly one of event, artifact, or attempt.
type InterventionTarget struct {
	EventID    string  `json:"event_id,omitempty"`
	ArtifactID string  `json:"artifact_id,omitempty"`
	AttemptID  string  `json:"attempt_id,omitempty"`
	Anchor     *Anchor `json:"anchor,omitempty"`
}

// InterveneRequest is the clean-break intervention payload. Legacy flat
// target_type/target_id payloads are accepted for compatibility.
type InterveneRequest struct {
	Target             InterventionTarget `json:"target"`
	Intent             string             `json:"intent"`
	Message            string             `json:"message"`
	ExpectedBranchHead string             `json:"expected_branch_head,omitempty"`
	IdempotencyKey     string             `json:"idempotency_key,omitempty"`
	Delivery           string             `json:"delivery,omitempty"`
}

func (request *InterveneRequest) UnmarshalJSON(data []byte) error {
	type raw InterveneRequest
	var nested struct {
		raw
		TargetType     string `json:"target_type"`
		TargetID       string `json:"target_id"`
		IdempotencyUnd string `json:"idempotency_key"`
	}
	_ = nested
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	var base raw
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	*request = InterveneRequest(base)
	if target, ok := probe["target"]; !ok || target == nil {
		legacyType, _ := probe["target_type"].(string)
		legacyID, _ := probe["target_id"].(string)
		switch legacyType {
		case "attempt":
			request.Target.AttemptID = legacyID
		case "event":
			request.Target.EventID = legacyID
		case "artifact":
			request.Target.ArtifactID = legacyID
		case "task", "":
			if legacyID != "" && legacyType == "task" {
				break
			}
		default:
			return fmt.Errorf("target_type must be task, attempt, event, or artifact")
		}
	}
	return nil
}

// Intervene persists the intervention and, for state-changing intents,
// creates a child branch plus queued attempt atomically.
func (s *Service) Intervene(ctx context.Context, taskID, actor string, request InterveneRequest) (store.InterventionResult, error) {
	request.Intent = strings.TrimSpace(strings.ToLower(request.Intent))
	request.Message = strings.TrimSpace(request.Message)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.ExpectedBranchHead = strings.TrimSpace(request.ExpectedBranchHead)
	if request.Intent == "" {
		return store.InterventionResult{}, fmt.Errorf("intent is required")
	}
	switch request.Intent {
	case "comment", "steer", "follow_up", "retry", "revise", "repair":
	default:
		return store.InterventionResult{}, fmt.Errorf("intent must be comment, steer, follow_up, retry, revise, or repair")
	}
	if request.IdempotencyKey == "" {
		return store.InterventionResult{}, fmt.Errorf("idempotency_key is required")
	}
	if request.Intent != "comment" && request.Intent != "retry" && request.Message == "" {
		return store.InterventionResult{}, ErrInvalidFeedback
	}
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.InterventionResult{}, err
	}
	selected := task.SelectedBranchID
	currentHead := s.db.TaskHeadAttempt(ctx, taskID)
	if request.ExpectedBranchHead != "" && request.ExpectedBranchHead != currentHead && !(selected == "" && currentHead == "") {
		return store.InterventionResult{}, store.ErrStaleBranch
	}
	if selected == "" && currentHead == "" && request.ExpectedBranchHead != "" {
		return store.InterventionResult{}, store.ErrStaleBranch
	}

	targetType, targetID, phase, err := s.resolveTarget(ctx, taskID, request.Target)
	if err != nil {
		return store.InterventionResult{}, err
	}
	if anchorErr := s.validateAnchor(ctx, taskID, request.Target); anchorErr != nil {
		return store.InterventionResult{}, anchorErr
	}
	if err = ValidateIntent(request.Intent, phase, task.State, request.Message); err != nil {
		return store.InterventionResult{}, err
	}

	delivery := "applied"
	if request.Intent == "steer" || request.Intent == "follow_up" {
		delivery = "queued"
		if request.Delivery != "" && request.Delivery != "steer" && request.Delivery != "follow_up" {
			return store.InterventionResult{}, fmt.Errorf("delivery is rejected for this target")
		}
	} else if request.Intent == "comment" {
		delivery = "applied"
	} else {
		delivery = "queued"
	}

	if request.Intent == "comment" || request.Intent == "steer" || request.Intent == "follow_up" {
		value := store.Intervention{ID: randomID(), TaskID: taskID, TargetType: targetType, TargetID: targetID, Actor: actor, Intent: request.Intent, Text: request.Message, Delivery: delivery, IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if request.Target.Anchor != nil {
			encoded, _ := json.Marshal(request.Target.Anchor)
			value.Anchor = string(encoded)
		}
		value.ExpectedHead = request.ExpectedBranchHead
		stored, created, err := s.db.SaveIntervention(ctx, value)
		if err != nil {
			return store.InterventionResult{}, err
		}
		if created {
			actions := AvailableActions(phase, task.State)
			_ = s.traceAttempt(ctx, taskID, phase, stored.ID, "intervention", "Intervention "+request.Intent, map[string]any{"intervention_id": stored.ID, "target_type": stored.TargetType, "target_id": stored.TargetID, "message": stored.Text, "delivery": stored.Delivery}, actions)
		}
		return store.InterventionResult{Intervention: stored, Action: request.Intent}, nil
	}

	// State-changing intents: retry, revise, repair.
	if task.State == string(Completed) || task.State == string(Aborted) {
		if active, activeErr := s.hasActiveTask(ctx, taskID); activeErr != nil {
			return store.InterventionResult{}, activeErr
		} else if active {
			return store.InterventionResult{}, store.ErrConflict
		}
	}
	if phase == nil {
		return store.InterventionResult{}, store.ErrConflict
	}
	snapshotDigest := phase.InputSnapshot
	if request.Intent == "repair" {
		snapshotDigest = phase.OutputSnapshot
		if snapshotDigest == "" {
			snapshotDigest = phase.InputSnapshot
		}
	}
	if snapshotDigest == "" {
		captured, captureErr := s.CaptureSnapshot(ctx, task)
		if captureErr != nil {
			return store.InterventionResult{}, captureErr
		}
		snapshotDigest = captured.Digest
	} else {
		if err = s.MaterializeSnapshot(ctx, task, snapshotDigest); err != nil {
			return store.InterventionResult{}, err
		}
	}

	parentBranch := selected
	if parentBranch == "" {
		branches, _ := s.db.Branches(ctx, taskID)
		if len(branches) > 0 {
			parentBranch = branches[0].ID
		}
	}
	branchID := randomID()
	branch := &store.Branch{ID: branchID, TaskID: taskID, ParentBranchID: parentBranch, ForkAttemptID: phase.ID, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	phases, _ := s.db.Phases(ctx, taskID)
	definitionID := phase.DefinitionID
	var definition *store.PhaseDefinition
	phaseKey := phase.Name
	revision := phase.DefinitionRev
	if request.Intent == "revise" {
		revision++
		if revision < 1 {
			revision = 1
		}
		definition = &store.PhaseDefinition{ID: randomID(), TaskID: taskID, PhaseKey: phaseKey, Revision: revision, Executor: phase.Kind, Owner: phase.Owner, Spec: `{"amendment":` + quoteJSON(request.Message) + `}`, Digest: planDigest(phaseKey, revision, request.Message), ParentRevision: phase.DefinitionRev, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		definitionID = definition.ID
	}
	if request.Intent == "repair" {
		phaseKey = "building"
	}
	attemptID := randomID()
	newPhase := &store.Phase{ID: attemptID, TaskID: taskID, Sequence: len(phases) + 1, Name: repairName(phase, request.Intent), Kind: repairKind(phase, request.Intent), Owner: repairOwner(phase, request.Intent), Description: "Intervention " + request.Intent + ": " + request.Message, Status: "queued", Attempt: phase.Attempt + 1, BranchID: branchID, DefinitionID: definitionID, InputSnapshot: snapshotDigest}
	anchorJSON := ""
	if request.Target.Anchor != nil {
		encoded, _ := json.Marshal(request.Target.Anchor)
		anchorJSON = string(encoded)
	}
	value := store.Intervention{ID: randomID(), TaskID: taskID, TargetType: targetType, TargetID: targetID, Actor: actor, Intent: request.Intent, Text: request.Message, Delivery: delivery, IdempotencyKey: request.IdempotencyKey, Anchor: anchorJSON, ExpectedHead: request.ExpectedBranchHead, BranchID: branchID, AttemptID: attemptID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	newState := string(Blocked)
	if task.State == string(Completed) || task.State == string(Aborted) {
		newState = string(Building)
		if phase.Kind == "check" {
			newState = string(Checking)
		}
	}
	applied, err := s.db.ApplyIntervention(ctx, value, branch, newPhase, definition, newState, task.State == string(Completed) || task.State == string(Aborted))
	if err != nil {
		return store.InterventionResult{}, err
	}
	if applied.Created {
		actions := []string{"comment", "pause", "abort"}
		_ = s.traceAttempt(ctx, taskID, newPhase, applied.Intervention.ID, "intervention", "Intervention "+request.Intent, map[string]any{"intervention_id": applied.Intervention.ID, "branch_id": branchID, "attempt_id": attemptID, "snapshot": snapshotDigest, "message": request.Message}, actions)
	}
	return store.InterventionResult{Intervention: applied.Intervention, BranchID: applied.BranchID, AttemptID: applied.AttemptID, Action: request.Intent}, nil
}

func (s *Service) resolveTarget(ctx context.Context, taskID string, target InterventionTarget) (string, string, *store.Phase, error) {
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

func (s *Service) validateAnchor(ctx context.Context, taskID string, target InterventionTarget) error {
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
			if anchor.Quote != "" && string(body[*anchor.Start:*anchor.End]) != anchor.Quote {
				if !strings.Contains(string(body), anchor.Quote) {
					return store.ErrStaleAnchor
				}
			}
		}
	case "json_pointer":
		if anchor.ValueHash != "" {
			// Value digest mismatch means the artifact changed under the anchor.
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

func (s *Service) hasActiveTask(ctx context.Context, exclude string) (bool, error) {
	tasks, err := s.db.Tasks(ctx)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.ID == exclude {
			continue
		}
		switch task.State {
		case string(Preparing), string(Planning), string(AwaitingApproval), string(Building), string(Checking), string(Reviewing):
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) traceAttempt(ctx context.Context, taskID string, phase *store.Phase, interventionID, eventType, name string, payload map[string]any, actions []string) error {
	phaseID, attemptID, branchID := "", "", ""
	if phase != nil {
		phaseID = phase.ID
		attemptID = phase.ID
		branchID = phase.BranchID
	}
	_, err := s.db.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: randomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, BranchID: branchID, Type: eventType, Name: name, Payload: payload, AvailableActions: actions, StartedAt: time.Now().UTC()})
	_ = interventionID
	return err
}

func repairName(phase *store.Phase, intent string) string {
	if intent == "repair" {
		return "building"
	}
	return phase.Name
}

func repairKind(phase *store.Phase, intent string) string {
	if intent == "repair" {
		return "agent"
	}
	return phase.Kind
}

func repairOwner(phase *store.Phase, intent string) string {
	if intent == "repair" {
		return "builder"
	}
	return phase.Owner
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func planDigest(key string, revision int, amendment string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", key, revision, amendment)))
	return fmt.Sprintf("%x", sum)
}

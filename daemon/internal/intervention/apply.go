package intervention

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Apply persists an intervention and, for state-changing intents, creates a
// child branch plus queued attempt atomically.
func (s *Service) Apply(ctx context.Context, taskID, actor string, request Request) (store.InterventionResult, error) {
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
	task, err := s.deps.Store.Task(ctx, taskID)
	if err != nil {
		return store.InterventionResult{}, err
	}
	selected := task.SelectedBranchID
	currentHead := s.deps.Store.TaskHeadAttempt(ctx, taskID)
	if request.ExpectedBranchHead != "" && request.ExpectedBranchHead != currentHead && !(selected == "" && currentHead == "") {
		return store.InterventionResult{}, store.ErrStaleBranch
	}
	if selected == "" && currentHead == "" && request.ExpectedBranchHead != "" {
		return store.InterventionResult{}, store.ErrStaleBranch
	}

	targetType, targetID, phase, err := s.Resolve(ctx, taskID, request.Target)
	if err != nil {
		return store.InterventionResult{}, err
	}
	if anchorErr := s.ValidateAnchor(ctx, taskID, request.Target); anchorErr != nil {
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
		value := store.Intervention{ID: stagekit.RandomID(), TaskID: taskID, TargetType: targetType, TargetID: targetID, Actor: actor, Intent: request.Intent, Text: request.Message, Delivery: delivery, IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if request.Target.Anchor != nil {
			encoded, _ := json.Marshal(request.Target.Anchor)
			value.Anchor = string(encoded)
		}
		value.ExpectedHead = request.ExpectedBranchHead
		stored, created, err := s.deps.Store.SaveIntervention(ctx, value)
		if err != nil {
			return store.InterventionResult{}, err
		}
		if created {
			actions := stagekit.AvailableActions(phase, task.State)
			_ = s.traceAttempt(ctx, taskID, phase, stored.ID, session.NewIntervention(session.InterventionPayload{Actor: stored.Actor, Intent: stored.Intent, Text: stored.Text, Delivery: stored.Delivery, InterventionID: stored.ID, TargetType: stored.TargetType, TargetID: stored.TargetID}), actions)
		}
		return store.InterventionResult{Intervention: stored, Action: request.Intent}, nil
	}

	// State-changing intents: retry, revise, repair.
	if task.State == string(stagekit.Completed) || task.State == string(stagekit.Aborted) {
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
		captured, captureErr := s.deps.Snapshots.CaptureSnapshot(ctx, task)
		if captureErr != nil {
			return store.InterventionResult{}, captureErr
		}
		snapshotDigest = captured.Digest
	} else {
		if err = s.deps.Snapshots.MaterializeSnapshot(ctx, task, snapshotDigest); err != nil {
			return store.InterventionResult{}, err
		}
	}

	parentBranch := selected
	if parentBranch == "" {
		branches, _ := s.deps.Store.Branches(ctx, taskID)
		if len(branches) > 0 {
			parentBranch = branches[0].ID
		}
	}
	branchID := stagekit.RandomID()
	branch := &store.Branch{ID: branchID, TaskID: taskID, ParentBranchID: parentBranch, ForkAttemptID: phase.ID, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	phases, _ := s.deps.Store.Phases(ctx, taskID)
	definitionID := phase.DefinitionID
	var definition *store.PhaseDefinition
	phaseKey := phase.Name
	phaseName := phase.Name
	phaseKind := phase.Kind
	phaseOwner := phase.Owner
	revision := phase.DefinitionRev
	if request.Intent == "revise" {
		revision++
		if revision < 1 {
			revision = 1
		}
		definition = &store.PhaseDefinition{ID: stagekit.RandomID(), TaskID: taskID, PhaseKey: phaseKey, Revision: revision, Executor: phase.Kind, Owner: phase.Owner, Spec: `{"amendment":` + quoteJSON(request.Message) + `}`, Digest: stagekit.PlanDigest(phaseKey, revision, request.Message), ParentRevision: phase.DefinitionRev, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		definitionID = definition.ID
	}
	if request.Intent == "repair" {
		_, pipeline, configErr := s.taskPipeline(task)
		if configErr != nil {
			return store.InterventionResult{}, configErr
		}
		buildStageFound := false
		for _, configuredStage := range pipeline.Stages {
			if configuredStage.Kind == "build" {
				buildStageFound = true
				phaseName = configuredStage.ID
				phaseKind = configuredStage.Kind
				phaseOwner = configuredStage.Agent
				if latest, latestErr := s.deps.Store.LatestDefinition(ctx, taskID, configuredStage.ID); latestErr == nil {
					definitionID = latest.ID
				} else {
					definitionID = ""
				}
				break
			}
		}
		if !buildStageFound {
			return store.InterventionResult{}, fmt.Errorf("task pipeline has no build stage")
		}
	}
	attemptID := stagekit.RandomID()
	newPhase := &store.Phase{ID: attemptID, TaskID: taskID, Sequence: len(phases) + 1, Name: phaseName, Kind: phaseKind, Owner: phaseOwner, Description: "Intervention " + request.Intent + ": " + request.Message, Status: "queued", Attempt: phase.Attempt + 1, BranchID: branchID, DefinitionID: definitionID, InputSnapshot: snapshotDigest}
	anchorJSON := ""
	if request.Target.Anchor != nil {
		encoded, _ := json.Marshal(request.Target.Anchor)
		anchorJSON = string(encoded)
	}
	value := store.Intervention{ID: stagekit.RandomID(), TaskID: taskID, TargetType: targetType, TargetID: targetID, Actor: actor, Intent: request.Intent, Text: request.Message, Delivery: delivery, IdempotencyKey: request.IdempotencyKey, Anchor: anchorJSON, ExpectedHead: request.ExpectedBranchHead, BranchID: branchID, AttemptID: attemptID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	newState := string(stagekit.StateForPhase(*newPhase))
	if request.Intent == "repair" {
		newState = string(stagekit.Building)
	}
	applied, err := s.deps.Store.ApplyIntervention(ctx, value, branch, newPhase, definition, newState, task.State == string(stagekit.Completed) || task.State == string(stagekit.Aborted))
	if err != nil {
		return store.InterventionResult{}, err
	}
	if applied.Created {
		actions := stagekit.AvailableActions(newPhase, newState)
		_ = s.traceAttempt(ctx, taskID, newPhase, applied.Intervention.ID, session.NewIntervention(session.InterventionPayload{Actor: applied.Intervention.Actor, Intent: applied.Intervention.Intent, Text: applied.Intervention.Text, Delivery: applied.Intervention.Delivery, InterventionID: applied.Intervention.ID, TargetType: applied.Intervention.TargetType, TargetID: applied.Intervention.TargetID}), actions)
	}
	return store.InterventionResult{Intervention: applied.Intervention, BranchID: applied.BranchID, AttemptID: applied.AttemptID, Action: request.Intent}, nil
}

func (s *Service) hasActiveTask(ctx context.Context, exclude string) (bool, error) {
	tasks, err := s.deps.Store.Tasks(ctx)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.ID == exclude {
			continue
		}
		if stagekit.IsActive(stagekit.State(task.State)) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) traceAttempt(ctx context.Context, taskID string, phase *store.Phase, interventionID string, entry session.Entry, actions []string) error {
	phaseID, attemptID, branchID := "", "", ""
	if phase != nil {
		phaseID = phase.ID
		attemptID = phase.ID
		branchID = phase.BranchID
	}
	_, err := s.deps.Store.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: stagekit.RandomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, BranchID: branchID, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display, AvailableActions: actions, StartedAt: time.Now().UTC()})
	_ = interventionID
	return err
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

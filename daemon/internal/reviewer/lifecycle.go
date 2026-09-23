package reviewer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

// savedReview resolves a durable review result eligible after the selected
// verification attempt.
func (s service) savedReview(ctx context.Context, taskID, verificationAttemptID string) (stage.ReviewResult, bool, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "review")
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	queued, err := s.kit.DB().QueuedMessageForStages(ctx, taskID, stageDef.ID, stageDef.Agent)
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	if queued {
		return stage.ReviewResult{}, false, nil
	}
	phase, ok, err := s.kit.SuccessfulPhase(ctx, task.ID, stageDef.ID)
	if err != nil || !ok {
		return stage.ReviewResult{}, false, err
	}
	payload, err := s.kit.PhaseEnvelope(ctx, task.ID, phase.ID)
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	review, err := Validate(payload)
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	eligible, err := s.kit.AttemptAfter(ctx, task.ID, phase.ID, verificationAttemptID)
	if err != nil {
		return stage.ReviewResult{}, false, err
	}
	if !eligible {
		return stage.ReviewResult{}, false, nil
	}
	return stage.ReviewResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: review.Approved}, true, nil
}

// beginReview starts the review attempt under the Reviewing state.
func (s service) beginReview(ctx context.Context, taskID, planAttemptID, buildAttemptID, verificationAttemptID string) (store.Task, store.Phase, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	planStage, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, planStage.ID, planAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	buildStage, err := s.kit.StageByKind(task, "build")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, buildStage.ID, buildAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	verifyStage, err := s.kit.StageByKind(task, "verify")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, verifyStage.ID, verificationAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stageDef, err := s.kit.StageByKind(task, "review")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.SetActiveStage(ctx, task.ID, stageDef.ID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task, err = s.kit.Task(ctx, task.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.Transition(ctx, task, stagekit.Reviewing, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(stagekit.Reviewing)
	phase, err := s.kit.BeginOrReusePhase(ctx, task.ID, stageDef.ID, stageDef.Kind, stageDef.Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// publishReview drains message turns, validates the verdict, enforces
// read-only observation, and applies the terminal transition.
func (s service) publishReview(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult, before string) (stage.ReviewResult, error) {
	validate := func(text string) (any, error) { return Validate(text) }
	drain := stagekit.DrainSpec{
		Task: task, Phase: phase, StageID: phase.Name, AgentName: phase.Owner, Role: "review",
		ReadOnly: true, Instructions: Instructions(), Validate: validate,
	}
	for {
		continued, err := s.kit.Drain(ctx, drain)
		if err != nil {
			s.kit.Fail(ctx, phase, err)
			return stage.ReviewResult{}, err
		}
		if continued.Payload != "" {
			turn = continued
		}
		payload := turn.Payload
		lock := s.kit.Lock(task.ID)
		lock.Lock()
		_, err = s.kit.DB().NextQueuedMessage(ctx, task.ID, phase.Name)
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			s.kit.Fail(ctx, phase, err)
			return stage.ReviewResult{}, err
		}
		review, validationErr := Validate(payload)
		if validationErr != nil {
			s.kit.Fail(ctx, phase, validationErr)
			lock.Unlock()
			return stage.ReviewResult{}, validationErr
		}
		if !review.Approved {
			rejected := fmt.Errorf("reviewer rejected implementation")
			err = s.kit.Complete(ctx, stagekit.Completion{
				Phase: phase, From: stagekit.Reviewing, To: stagekit.Blocked, Status: "failed", Cause: rejected,
			})
			lock.Unlock()
			if err != nil {
				return stage.ReviewResult{}, err
			}
			return stage.ReviewResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: false}, nil
		}
		after, fingerprintErr := workspace.Fingerprint(ctx, s.kit.Git(), task)
		if fingerprintErr != nil {
			s.kit.Fail(ctx, phase, fingerprintErr)
			lock.Unlock()
			return stage.ReviewResult{}, fingerprintErr
		}
		if before != after {
			readonlyErr := fmt.Errorf("%s modified repository", phase.Name)
			s.kit.Fail(ctx, phase, readonlyErr)
			lock.Unlock()
			return stage.ReviewResult{}, readonlyErr
		}
		err = s.kit.Complete(ctx, stagekit.Completion{
			Phase: phase, From: stagekit.Reviewing, To: stagekit.Completed, Status: "success",
		})
		if err != nil {
			s.kit.Fail(ctx, phase, err)
		}
		lock.Unlock()
		if err != nil {
			return stage.ReviewResult{}, err
		}
		return stage.ReviewResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: true}, nil
	}
}

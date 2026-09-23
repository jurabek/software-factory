package planner

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

var errNoDurableResult = errors.New("planner completed without a durable result")

// savedPlan resolves a durable planning result, or reports that planning must
// run. A queued message for the planner forces a fresh turn.
func (s service) savedPlan(ctx context.Context, taskID string) (stage.PlanResult, bool, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	queued, err := s.kit.DB().QueuedMessageForStages(ctx, taskID, stageDef.ID, stageDef.Agent)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	if queued {
		return stage.PlanResult{}, false, nil
	}
	phase, ok, err := s.kit.SuccessfulPhase(ctx, taskID, stageDef.ID)
	if err != nil || !ok {
		return stage.PlanResult{}, false, err
	}
	payload, err := s.kit.PhaseEnvelope(ctx, taskID, phase.ID)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	return stage.PlanResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: task.ApprovalActor != ""}, true, nil
}

// beginPlan starts or reuses the planning attempt after the creation stage has
// prepared the repository.
func (s service) beginPlan(ctx context.Context, taskID string) (store.Task, store.Phase, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if task.State == string(stagekit.Preparing) {
		if err = s.kit.Transition(ctx, task, stagekit.Planning, ""); err != nil {
			return store.Task{}, store.Phase{}, err
		}
		task.State = string(stagekit.Planning)
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.SetActiveStage(ctx, task.ID, stageDef.ID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	phase, err := s.kit.BeginOrReusePhase(ctx, task.ID, stageDef.ID, stageDef.Kind, stageDef.Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// publishPlan delivers message turns, enforces read-only observation, and transitions to AwaitingApproval.
func (s service) publishPlan(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult) (stage.PlanResult, error) {
	baseline, err := workspace.Fingerprint(task)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	return s.kit.DeliverAndFinalize(ctx, stagekit.Delivery{
		Task: task, Phase: phase, Role: "planner", ReadOnly: true, Instructions: Instructions(),
		Validate: func(text string) (any, error) { return Validate(text) },
	}, turn, func(turn harness.TurnResult) (stage.PlanResult, error) {
		after, changedErr := workspace.Fingerprint(task)
		if changedErr != nil {
			s.kit.Fail(ctx, phase, changedErr)
			return stage.PlanResult{}, changedErr
		}
		if baseline != after {
			readonlyErr := fmt.Errorf("planner modified repository")
			s.kit.Fail(ctx, phase, readonlyErr)
			return stage.PlanResult{}, readonlyErr
		}
		if completeErr := s.kit.Complete(ctx, stagekit.Completion{
			Phase: phase, From: stagekit.Planning, To: stagekit.AwaitingApproval, Status: "success",
			Approval: stagekit.PlanApprovalDigest(turn.Payload), Planner: true,
		}); completeErr != nil {
			s.kit.Fail(ctx, phase, completeErr)
			return stage.PlanResult{}, completeErr
		}
		refreshed, err := s.kit.Task(ctx, task.ID)
		if err != nil {
			return stage.PlanResult{}, err
		}
		result, ok, err := s.savedPlan(ctx, refreshed.ID)
		if err != nil {
			return stage.PlanResult{}, err
		}
		if !ok {
			return stage.PlanResult{}, errNoDurableResult
		}
		return result, nil
	})
}

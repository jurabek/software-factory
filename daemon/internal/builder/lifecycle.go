package builder

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

// savedBuild resolves a durable build result eligible after the selected plan.
func (s service) savedBuild(ctx context.Context, taskID, planAttemptID string) (stage.BuildResult, bool, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	planStage, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, planStage.ID, planAttemptID); err != nil {
		return stage.BuildResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "build")
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	queued, err := s.kit.DB().QueuedMessageForStages(ctx, taskID, stageDef.ID, "builder")
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	if queued {
		return stage.BuildResult{}, false, nil
	}
	phase, ok, err := s.kit.SuccessfulPhase(ctx, task.ID, stageDef.ID)
	if err != nil || !ok {
		return stage.BuildResult{}, false, err
	}
	payload, err := s.kit.PhaseEnvelope(ctx, task.ID, phase.ID)
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	eligible, err := s.kit.AttemptAfter(ctx, task.ID, phase.ID, planAttemptID)
	if err != nil {
		return stage.BuildResult{}, false, err
	}
	if !eligible {
		return stage.BuildResult{}, false, nil
	}
	return stage.BuildResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot}, true, nil
}

// beginBuild starts or reuses the build attempt under the Building state.
func (s service) beginBuild(ctx context.Context, taskID, planAttemptID string) (store.Task, store.Phase, error) {
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
	stageDef, err := s.kit.StageByKind(task, "build")
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
	if err = s.kit.Transition(ctx, task, stagekit.Building, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(stagekit.Building)
	phase, err := s.kit.BeginOrReusePhase(ctx, task.ID, stageDef.ID, stageDef.Kind, stageDef.Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// publishBuild drains message turns, enforces protected paths, persists test
// evidence, and transitions to Checking.
func (s service) publishBuild(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult, profile workspace.Materialization) (stage.BuildResult, error) {
	validate := func(text string) (any, error) {
		return ValidateWithEvidence(ctx, s.kit.Git(), task.RepositoryPath, workspace.ReviewBase(task), profile.Tests, text)
	}
	persist := func(ctx context.Context, text string) error {
		return PersistEvidence(ctx, s.kit.Git(), s.kit.DB(), task, phase, text)
	}
	drain := stagekit.DrainSpec{
		Task: task, Phase: phase, StageID: phase.Name, AgentName: phase.Owner, Role: "build",
		ReadOnly: readOnly(phase), Instructions: Instructions(), Validate: validate, OnValid: persist,
	}
	for {
		continued, err := s.kit.Drain(ctx, drain)
		if err != nil {
			s.kit.Fail(ctx, phase, err)
			return stage.BuildResult{}, err
		}
		if continued.Payload != "" {
			turn = continued
		}
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
			return stage.BuildResult{}, err
		}
		if err = s.validatePaths(ctx, task, profile); err != nil {
			s.kit.Fail(ctx, phase, err)
			lock.Unlock()
			return stage.BuildResult{}, err
		}
		if err = PersistEvidence(ctx, s.kit.Git(), s.kit.DB(), task, phase, turn.Payload); err != nil {
			s.kit.Fail(ctx, phase, err)
			lock.Unlock()
			return stage.BuildResult{}, err
		}
		err = s.kit.Complete(ctx, stagekit.Completion{
			Phase: phase, From: stagekit.Building, To: stagekit.Checking, Status: "success",
		})
		if err != nil {
			s.kit.Fail(ctx, phase, err)
		}
		lock.Unlock()
		if err != nil {
			return stage.BuildResult{}, err
		}
		resultPhase, ok, err := s.kit.SuccessfulPhase(ctx, task.ID, phase.Name)
		if err != nil {
			return stage.BuildResult{}, err
		}
		if !ok {
			return stage.BuildResult{}, errNoDurableResult
		}
		resultPayload, err := s.kit.PhaseEnvelope(ctx, task.ID, resultPhase.ID)
		if err != nil {
			return stage.BuildResult{}, err
		}
		return stage.BuildResult{Payload: resultPayload, AttemptID: resultPhase.ID, SnapshotID: resultPhase.OutputSnapshot}, nil
	}
}

func (s service) validatePaths(ctx context.Context, task store.Task, profile workspace.Materialization) error {
	return CheckProtectedPaths(ctx, s.kit.Git(), task.RepositoryPath, workspace.ReviewBase(task), profile.Protected)
}

var errNoDurableResult = fmt.Errorf("builder completed without a durable result")

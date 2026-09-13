package verifier

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// savedVerification resolves a durable verification result eligible after the
// selected build.
func (s service) savedVerification(ctx context.Context, taskID, buildAttemptID string) (stage.VerificationResult, bool, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return stage.VerificationResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "verify")
	if err != nil {
		return stage.VerificationResult{}, false, err
	}
	phase, ok, err := s.kit.SuccessfulPhase(ctx, task.ID, stageDef.ID)
	if err != nil || !ok {
		return stage.VerificationResult{}, false, err
	}
	eligible, err := s.kit.AttemptAfter(ctx, task.ID, phase.ID, buildAttemptID)
	if err != nil {
		return stage.VerificationResult{}, false, err
	}
	if !eligible {
		return stage.VerificationResult{}, false, nil
	}
	checks, err := s.kit.DB().Checks(ctx, task.ID)
	if err != nil {
		return stage.VerificationResult{}, false, err
	}
	passed := true
	for _, check := range checks {
		if check.PhaseID == phase.ID && check.Status != "passed" {
			passed = false
		}
	}
	return stage.VerificationResult{AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Passed: passed}, true, nil
}

// beginVerification starts the verify attempt under the Checking state.
func (s service) beginVerification(ctx context.Context, taskID, planAttemptID, buildAttemptID string) (store.Task, store.Phase, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, "planning", planAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	buildStage, err := s.kit.StageByKind(task, "build")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.RequireAttempt(ctx, task.ID, buildStage.ID, buildAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stageDef, err := s.kit.StageByKind(task, "verify")
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
	if err = s.kit.Transition(ctx, task, stagekit.Checking, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(stagekit.Checking)
	phase, err := s.kit.BeginOrReusePhase(ctx, task.ID, stageDef.ID, stageDef.Kind, stageDef.Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// publishVerification persists the report with its checks/comparisons evidence
// and applies the verification verdict's terminal transition.
func (s service) publishVerification(ctx context.Context, task store.Task, phase store.Phase, checks []store.Check, comparisons []store.Comparison, report string, passed bool) (stage.VerificationResult, error) {
	artifact := s.kit.ReportArtifact(task, phase, "verification", report, "deterministic-checks")
	status, to := "success", stagekit.Reviewing
	if !passed {
		status, to = "failed", stagekit.Blocked
	}
	if err := s.kit.Complete(ctx, stagekit.Completion{
		Phase: phase, From: stagekit.Checking, To: to, Status: status,
		Checks: checks, Comparisons: comparisons, Artifact: &artifact,
	}); err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.VerificationResult{}, err
	}
	return stage.VerificationResult{AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Report: report, Passed: passed}, nil
}

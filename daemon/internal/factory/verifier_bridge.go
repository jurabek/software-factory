package factory

import (
	"context"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/verifier"
)

// Verifier composes the verification stage with narrow collaborators. Check
// execution, comparisons, and report rendering live in the verifier package;
// phase lifecycle and atomic publication stay here until Pipeline owns
// checkpoints.
func (s *Service) Verifier() verifier.Service {
	return verifier.New(verifier.Deps{
		Checks:    s.db,
		Git:       s.git,
		Snapshots: s.snapshots,
		Publisher: s,
	})
}

func (s *Service) SavedVerification(ctx context.Context, taskID, buildAttemptID string) (pipeline.VerificationResult, bool, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return pipeline.VerificationResult{}, false, err
	}
	stage, err := s.stageByKind(task, "verify")
	if err != nil {
		return pipeline.VerificationResult{}, false, err
	}
	result, ok, err := s.savedVerification(ctx, task.ID, stage.ID)
	if err != nil || !ok {
		return result, ok, err
	}
	eligible, err := s.attemptAfter(ctx, task.ID, result.AttemptID, buildAttemptID)
	if err != nil {
		return pipeline.VerificationResult{}, false, err
	}
	if eligible {
		return result, true, nil
	}
	return pipeline.VerificationResult{}, false, nil
}

func (s *Service) BeginVerification(ctx context.Context, taskID, planAttemptID, buildAttemptID string) (store.Task, store.Phase, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.requireAttempt(ctx, task.ID, "planning", planAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	buildStage, err := s.stageByKind(task, "build")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.requireAttempt(ctx, task.ID, buildStage.ID, buildAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stage, err := s.stageByKind(task, "verify")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.db.SetActiveStage(ctx, task.ID, stage.ID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task, err = s.db.Task(ctx, task.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.transition(ctx, task, Checking, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(Checking)
	phase, err := s.beginPhase(ctx, task.ID, stage.ID, stage.Kind, stage.Agent, "Execute "+stage.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// PublishVerification atomically persists the verification report with its
// checks/comparisons evidence and transitions to review. Evidence rows are
// written by the verifier package during execution; the phase transition
// stays here until Pipeline owns checkpoints.
func (s *Service) PublishVerification(ctx context.Context, task store.Task, phase store.Phase, checks []store.Check, comparisons []store.Comparison, report string) (pipeline.VerificationResult, error) {
	artifact := s.reportArtifact(task, phase, "verification", report, "deterministic-checks")
	if err := s.completeVerificationPhaseTransitionWithArtifact(ctx, phase, Checking, Reviewing, "success", nil, checks, comparisons, &artifact); err != nil {
		s.failPhase(ctx, phase, err)
		return pipeline.VerificationResult{}, err
	}
	result, ok, err := s.savedVerification(ctx, task.ID, phase.Name)
	if err != nil {
		return pipeline.VerificationResult{}, err
	}
	if !ok {
		return pipeline.VerificationResult{}, fmt.Errorf("verifier completed without a durable result")
	}
	return result, nil
}

func (s *Service) FailVerification(ctx context.Context, phase store.Phase, cause error) {
	s.failPhase(ctx, phase, cause)
}

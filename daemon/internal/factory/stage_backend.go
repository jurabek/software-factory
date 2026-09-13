package factory

import (
	"context"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func (s *Service) stageByKind(task store.Task, kind string) (config.Stage, error) {
	_, configured, err := s.pipelines.taskPipeline(task)
	if err != nil {
		return config.Stage{}, err
	}
	for _, stage := range configured.Stages {
		if stage.Kind == kind {
			return stage, nil
		}
	}
	return config.Stage{}, fmt.Errorf("task pipeline has no %s stage", kind)
}

func (s *Service) successfulPhase(ctx context.Context, taskID, name string) (store.Phase, bool, error) {
	phase, ok, err := s.latestStageAttempt(ctx, taskID, name)
	if err != nil || !ok || phase.Status != "success" {
		return store.Phase{}, false, err
	}
	return phase, true, nil
}

func (s *Service) requireAttempt(ctx context.Context, taskID, name, attemptID string) error {
	phase, ok, err := s.successfulPhase(ctx, taskID, name)
	if err != nil {
		return err
	}
	if !ok || phase.ID != attemptID {
		return fmt.Errorf("%s result %s is no longer eligible", name, attemptID)
	}
	return nil
}

func (s *Service) attemptAfter(ctx context.Context, taskID, attemptID, upstreamID string) (bool, error) {
	attempt, err := s.db.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return false, err
	}
	upstream, err := s.db.PhaseByID(ctx, taskID, upstreamID)
	if err != nil {
		return false, err
	}
	// Lineage policy is owned by Pipeline: downstream results are eligible
	// only when produced after their upstream result.
	return pipeline.EligibleAfter(attempt.Sequence, upstream.Sequence), nil
}

func (s *Service) phaseEnvelope(ctx context.Context, taskID, phaseID string) (string, error) {
	envelopes, err := s.db.Envelopes(ctx, taskID)
	if err != nil {
		return "", err
	}
	for index := len(envelopes) - 1; index >= 0; index-- {
		if envelopes[index].PhaseID == phaseID && envelopes[index].Valid {
			return envelopes[index].Payload, nil
		}
	}
	return "", store.ErrNotFound
}

func (s *Service) savedPlan(ctx context.Context, task store.Task) (pipeline.PlanResult, bool, error) {
	phase, ok, err := s.successfulPhase(ctx, task.ID, "planning")
	if err != nil || !ok {
		return pipeline.PlanResult{}, false, err
	}
	payload, err := s.phaseEnvelope(ctx, task.ID, phase.ID)
	if err != nil {
		return pipeline.PlanResult{}, false, err
	}
	return pipeline.PlanResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: task.ApprovalActor != ""}, true, nil
}

func (s *Service) savedBuild(ctx context.Context, taskID, stageID string) (pipeline.BuildResult, bool, error) {
	phase, ok, err := s.successfulPhase(ctx, taskID, stageID)
	if err != nil || !ok {
		return pipeline.BuildResult{}, false, err
	}
	payload, err := s.phaseEnvelope(ctx, taskID, phase.ID)
	if err != nil {
		return pipeline.BuildResult{}, false, err
	}
	return pipeline.BuildResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot}, true, nil
}

func (s *Service) savedVerification(ctx context.Context, taskID, stageID string) (pipeline.VerificationResult, bool, error) {
	phase, ok, err := s.successfulPhase(ctx, taskID, stageID)
	if err != nil || !ok {
		return pipeline.VerificationResult{}, false, err
	}
	checks, err := s.db.Checks(ctx, taskID)
	if err != nil {
		return pipeline.VerificationResult{}, false, err
	}
	passed := true
	for _, check := range checks {
		if check.PhaseID == phase.ID && check.Status != "passed" {
			passed = false
		}
	}
	return pipeline.VerificationResult{AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Passed: passed}, true, nil
}

func (s *Service) savedReview(ctx context.Context, taskID, stageID string) (pipeline.ReviewResult, bool, error) {
	phase, ok, err := s.successfulPhase(ctx, taskID, stageID)
	if err != nil || !ok {
		return pipeline.ReviewResult{}, false, err
	}
	payload, err := s.phaseEnvelope(ctx, taskID, phase.ID)
	if err != nil {
		return pipeline.ReviewResult{}, false, err
	}
	review, err := ValidateReview(payload)
	if err != nil {
		return pipeline.ReviewResult{}, false, err
	}
	return pipeline.ReviewResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: review.Approved}, true, nil
}

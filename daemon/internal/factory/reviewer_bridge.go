package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/reviewer"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Reviewer composes the review stage with narrow collaborators. Evidence
// assembly, verdict validation, and turn execution live in the reviewer
// package; phase lifecycle and atomic publication stay here until Pipeline
// owns checkpoints.
func (s *Service) Reviewer() reviewer.Service {
	return reviewer.New(reviewer.Deps{
		Turner:     agentexec.Deps{DB: s.db, Harnesses: s.harnesses, Git: s.git},
		Sinks:      s.eventSink,
		Configurer: plannerConfigurer{service: s},
		Publisher:  s,
		Evidence:   reviewerEvidence{service: s},
		Git:        s.git,
	})
}

// reviewerEvidence adapts orchestration reads to the reviewer-owned evidence
// surface. Assembly of the review prompt payload lives in the reviewer
// package; this adapter only reads.
type reviewerEvidence struct{ service *Service }

func (e reviewerEvidence) Checks(ctx context.Context, taskID string) ([]store.Check, error) {
	return e.service.db.Checks(ctx, taskID)
}

func (e reviewerEvidence) TestChanges(ctx context.Context, taskID string) ([]store.TestChange, error) {
	return e.service.db.TestChanges(ctx, taskID)
}

func (e reviewerEvidence) Comparisons(ctx context.Context, taskID string) ([]store.Comparison, error) {
	return e.service.db.Comparisons(ctx, taskID)
}

func (e reviewerEvidence) ChangedFiles(ctx context.Context, taskID string) ([]string, error) {
	task, err := e.service.db.Task(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return taskChangedFiles(ctx, e.service.git, task, true)
}

func (e reviewerEvidence) Diff(ctx context.Context, taskID string) (reviewer.Diff, error) {
	task, err := e.service.db.Task(ctx, taskID)
	if err != nil {
		return reviewer.Diff{}, err
	}
	changes, err := e.service.tasks.diffRepository(ctx, task, true)
	if err != nil {
		return reviewer.Diff{}, err
	}
	return reviewer.Diff{Files: changes.Files, Patch: changes.Patch}, nil
}

func (s *Service) SavedReview(ctx context.Context, taskID, verificationAttemptID string) (pipeline.ReviewResult, bool, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return pipeline.ReviewResult{}, false, err
	}
	stage, err := s.stageByKind(task, "review")
	if err != nil {
		return pipeline.ReviewResult{}, false, err
	}
	result, ok, err := s.savedReview(ctx, task.ID, stage.ID)
	if err != nil || !ok {
		return result, ok, err
	}
	eligible, err := s.attemptAfter(ctx, task.ID, result.AttemptID, verificationAttemptID)
	if err != nil {
		return pipeline.ReviewResult{}, false, err
	}
	if eligible {
		return result, true, nil
	}
	return pipeline.ReviewResult{}, false, nil
}

func (s *Service) BeginReview(ctx context.Context, taskID, planAttemptID, buildAttemptID, verificationAttemptID string) (store.Task, store.Phase, error) {
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
	verifyStage, err := s.stageByKind(task, "verify")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.requireAttempt(ctx, task.ID, verifyStage.ID, verificationAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stage, err := s.stageByKind(task, "review")
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
	if err = s.transition(ctx, task, Reviewing, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(Reviewing)
	phase, err := s.beginPhase(ctx, task.ID, stage.ID, stage.Kind, stage.Agent, "Execute "+stage.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// PublishReview drains message turns, then atomically validates the verdict,
// enforces read-only observation, and transitions to completion.
// Message/completion synchronization stays here until Pipeline owns
// checkpoints.
func (s *Service) PublishReview(ctx context.Context, task store.Task, phase store.Phase, payload, before string) (pipeline.ReviewResult, error) {
	validate := validatorForRole("reviewer")
	for {
		continued, err := s.drainMessages(ctx, task, phase, phase.Name, phase.Owner, validate)
		if err != nil {
			s.failPhase(ctx, phase, err)
			return pipeline.ReviewResult{}, err
		}
		if continued != "" {
			payload = continued
		}
		lock := s.taskLock(task.ID)
		lock.Lock()
		_, err = s.db.NextQueuedMessage(ctx, task.ID, phase.Name)
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			s.failPhase(ctx, phase, err)
			return pipeline.ReviewResult{}, err
		}
		review, validationErr := ValidateReview(payload)
		if validationErr != nil {
			s.failPhase(ctx, phase, validationErr)
			lock.Unlock()
			return pipeline.ReviewResult{}, validationErr
		}
		if !review.Approved {
			rejected := fmt.Errorf("reviewer rejected implementation")
			s.failPhase(ctx, phase, rejected)
			lock.Unlock()
			return pipeline.ReviewResult{}, rejected
		}
		after, fingerprintErr := repositoryFingerprint(ctx, s.git, task)
		if fingerprintErr != nil {
			s.failPhase(ctx, phase, fingerprintErr)
			lock.Unlock()
			return pipeline.ReviewResult{}, fingerprintErr
		}
		if before != after {
			readonlyErr := fmt.Errorf("%s modified repository", phase.Name)
			s.failPhase(ctx, phase, readonlyErr)
			lock.Unlock()
			return pipeline.ReviewResult{}, readonlyErr
		}
		artifact, artifactErr := s.agentReportArtifact(task, phase, "review", payload)
		if artifactErr != nil {
			s.failPhase(ctx, phase, artifactErr)
			lock.Unlock()
			return pipeline.ReviewResult{}, artifactErr
		}
		err = s.completePhaseTransitionWithArtifact(ctx, phase, stateForPhase(phase), Completed, "success", nil, &artifact)
		if err != nil {
			s.failPhase(ctx, phase, err)
		}
		lock.Unlock()
		if err != nil {
			return pipeline.ReviewResult{}, err
		}
		result, ok, err := s.savedReview(ctx, task.ID, phase.Name)
		if err != nil {
			return pipeline.ReviewResult{}, err
		}
		if !ok {
			return pipeline.ReviewResult{}, fmt.Errorf("reviewer completed without a durable result")
		}
		return result, nil
	}
}

func (s *Service) FailReview(ctx context.Context, phase store.Phase, cause error) {
	s.failPhase(ctx, phase, cause)
}

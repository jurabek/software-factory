package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/builder"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Builder composes the implementation stage with narrow collaborators. Prompt
// data, test evidence validation, and turn execution live in the builder
// package; phase lifecycle and atomic publication stay here until Pipeline
// owns checkpoints.
func (s *Service) Builder() builder.Service {
	return builder.New(builder.Deps{
		Turner:     agentexec.Deps{DB: s.db, Harnesses: s.harnesses, Git: s.git},
		Sinks:      s.eventSink,
		Configurer: plannerConfigurer{service: s},
		Publisher:  s,
	})
}

func (s *Service) SavedBuild(ctx context.Context, taskID, planAttemptID string) (pipeline.BuildResult, bool, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return pipeline.BuildResult{}, false, err
	}
	if err = s.requireAttempt(ctx, task.ID, "planning", planAttemptID); err != nil {
		return pipeline.BuildResult{}, false, err
	}
	stage, err := s.stageByKind(task, "build")
	if err != nil {
		return pipeline.BuildResult{}, false, err
	}
	result, ok, err := s.savedBuild(ctx, task.ID, stage.ID)
	if err != nil || !ok {
		return result, ok, err
	}
	eligible, err := s.attemptAfter(ctx, task.ID, result.AttemptID, planAttemptID)
	if err != nil {
		return pipeline.BuildResult{}, false, err
	}
	if eligible {
		return result, true, nil
	}
	return pipeline.BuildResult{}, false, nil
}

func (s *Service) BeginBuild(ctx context.Context, taskID, planAttemptID string) (store.Task, store.Phase, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.requireAttempt(ctx, task.ID, "planning", planAttemptID); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	stage, err := s.stageByKind(task, "build")
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
	if err = s.transition(ctx, task, Building, ""); err != nil {
		return store.Task{}, store.Phase{}, err
	}
	task.State = string(Building)
	phase, err := s.beginPhase(ctx, task.ID, stage.ID, stage.Kind, stage.Agent, "Execute "+stage.ID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// PublishBuild drains message turns, then atomically enforces protected
// paths, persists test evidence, and transitions to verification.
// Message/completion synchronization stays here until Pipeline owns
// checkpoints.
func (s *Service) PublishBuild(ctx context.Context, task store.Task, phase store.Phase, payload string) (pipeline.BuildResult, error) {
	profile, err := readTaskProfile(task)
	if err != nil {
		return pipeline.BuildResult{}, err
	}
	validate := s.quality.builderValidator(ctx, task, profile)
	for {
		continued, err := s.drainMessages(ctx, task, phase, phase.Name, phase.Owner, validate)
		if err != nil {
			s.failPhase(ctx, phase, err)
			return pipeline.BuildResult{}, err
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
			return pipeline.BuildResult{}, err
		}
		if err = s.validateBuilderPaths(ctx, task, profile); err != nil {
			s.failPhase(ctx, phase, err)
			lock.Unlock()
			return pipeline.BuildResult{}, err
		}
		if err = s.quality.persistBuilderEvidence(ctx, task, phase, payload); err != nil {
			s.failPhase(ctx, phase, err)
			lock.Unlock()
			return pipeline.BuildResult{}, err
		}
		artifact, artifactErr := s.agentReportArtifact(task, phase, "build", payload)
		if artifactErr != nil {
			s.failPhase(ctx, phase, artifactErr)
			lock.Unlock()
			return pipeline.BuildResult{}, artifactErr
		}
		err = s.completePhaseTransitionWithArtifact(ctx, phase, stateForPhase(phase), Checking, "success", nil, &artifact)
		if err != nil {
			s.failPhase(ctx, phase, err)
		}
		lock.Unlock()
		if err != nil {
			return pipeline.BuildResult{}, err
		}
		result, ok, err := s.savedBuild(ctx, task.ID, phase.Name)
		if err != nil {
			return pipeline.BuildResult{}, err
		}
		if !ok {
			return pipeline.BuildResult{}, fmt.Errorf("builder completed without a durable result")
		}
		return result, nil
	}
}

func (s *Service) FailBuild(ctx context.Context, phase store.Phase, cause error) {
	s.failPhase(ctx, phase, cause)
}

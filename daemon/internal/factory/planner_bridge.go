package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/planner"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// SetPipeliner injects the composed workflow after stage services are built.
func (s *Service) SetPipeliner(pipeliner Pipeliner) {
	s.pipeliner = pipeliner
}

type plannerConfigurer struct{ service *Service }

func (c plannerConfigurer) TaskConfig(_ context.Context, task store.Task) (agentexec.TaskConfig, error) {
	configured, err := c.service.taskConfig(task)
	if err != nil {
		return agentexec.TaskConfig{}, err
	}
	return agentexec.TaskConfig{Config: configured, ConfigPath: c.service.configPath, TaskDir: c.service.taskDir(task.ID)}, nil
}

// Planner composes the planning stage with narrow collaborators. Prompt data,
// validation, and turn execution live in the planner package; phase lifecycle
// and atomic publication stay here until Pipeline owns checkpoints.
func (s *Service) Planner() planner.Service {
	return planner.New(planner.Deps{
		Turner:     agentexec.Deps{DB: s.db, Harnesses: s.harnesses, Git: s.git},
		Sinks:      s.eventSink,
		Configurer: plannerConfigurer{service: s},
		Publisher:  s,
	})
}

func (s *Service) SavedPlan(ctx context.Context, taskID string) (pipeline.PlanResult, bool, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return pipeline.PlanResult{}, false, err
	}
	return s.savedPlan(ctx, task)
}

func (s *Service) BeginPlan(ctx context.Context, taskID string) (store.Task, store.Phase, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if task.RepositoryPath == "" {
		if err = s.prepareOnly(ctx, task); err != nil {
			return store.Task{}, store.Phase{}, err
		}
		task, err = s.db.Task(ctx, taskID)
		if err != nil {
			return store.Task{}, store.Phase{}, err
		}
	}
	if task.State == string(Preparing) {
		if err = s.db.Transition(ctx, task.ID, task.State, string(Planning), task.ActivePhase, ""); err != nil {
			return store.Task{}, store.Phase{}, err
		}
		task.State = string(Planning)
	}
	phase, err := s.beginPhase(ctx, task.ID, "planning", "agent", "planner", "Create implementation plan")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	return task, phase, nil
}

// PublishPlan drains message turns, then atomically verifies, publishes the
// plan report, and transitions to approval. Message/completion
// synchronization stays here until Pipeline owns checkpoints.
func (s *Service) PublishPlan(ctx context.Context, task store.Task, phase store.Phase, payload string) (pipeline.PlanResult, error) {
	baseline, err := repositoryFingerprint(ctx, s.git, task)
	if err != nil {
		return pipeline.PlanResult{}, err
	}
	validate := validatorForRole("planner")
	for {
		continued, err := s.drainMessages(ctx, task, phase, "planner", "planner", validate)
		if err != nil {
			s.failPhase(ctx, phase, err)
			return pipeline.PlanResult{}, err
		}
		if continued != "" {
			payload = continued
		}
		lock := s.taskLock(task.ID)
		lock.Lock()
		_, err = s.db.NextQueuedMessage(ctx, task.ID, "planner")
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			s.failPhase(ctx, phase, err)
			return pipeline.PlanResult{}, err
		}
		after, changedErr := repositoryFingerprint(ctx, s.git, task)
		if changedErr != nil {
			s.failPhase(ctx, phase, changedErr)
			lock.Unlock()
			return pipeline.PlanResult{}, changedErr
		}
		if baseline != after {
			readonlyErr := fmt.Errorf("planner modified repository")
			s.failPhase(ctx, phase, readonlyErr)
			lock.Unlock()
			return pipeline.PlanResult{}, readonlyErr
		}
		artifact, artifactErr := s.agentReportArtifact(task, phase, "planner", payload)
		if artifactErr != nil {
			s.failPhase(ctx, phase, artifactErr)
			lock.Unlock()
			return pipeline.PlanResult{}, artifactErr
		}
		err = s.completePlannerPhase(ctx, phase, stateForPhase(phase), AwaitingApproval, "success", nil, planApprovalDigest(payload, artifact.Digest), &artifact)
		if err != nil {
			s.failPhase(ctx, phase, err)
		}
		lock.Unlock()
		if err != nil {
			return pipeline.PlanResult{}, err
		}
		task, err = s.db.Task(ctx, task.ID)
		if err != nil {
			return pipeline.PlanResult{}, err
		}
		result, ok, err := s.savedPlan(ctx, task)
		if err != nil {
			return pipeline.PlanResult{}, err
		}
		if !ok {
			return pipeline.PlanResult{}, fmt.Errorf("planner completed without a durable result")
		}
		return result, nil
	}
}

func (s *Service) FailPlan(ctx context.Context, phase store.Phase, cause error) {
	s.failPhase(ctx, phase, cause)
}

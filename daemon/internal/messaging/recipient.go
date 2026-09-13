package messaging

import (
	"context"
	"slices"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func (s *Service) messageRecipient(ctx context.Context, task store.Task, target *store.Phase) (string, *store.Phase, error) {
	if target != nil && target.Kind == "agent" {
		return target.Owner, target, nil
	}
	if target != nil && target.Name != "" && target.Kind != "check" && target.Kind != "git" {
		return target.Name, target, nil
	}
	if task.ActivePhase != "" {
		active, err := s.deps.Store.PhaseByID(ctx, task.ID, task.ActivePhase)
		if err == nil && active.Status == "running" && active.Kind != "check" && active.Kind != "git" {
			if active.Kind == "agent" {
				return active.Owner, &active, nil
			}
			return active.Name, &active, nil
		}
	}
	phases, err := s.deps.Store.Phases(ctx, task.ID)
	if err != nil {
		return "", nil, err
	}
	var latest *store.Phase
	if len(phases) > 0 {
		value := phases[len(phases)-1]
		latest = &value
	}
	state := stagekit.State(task.State)
	if state == stagekit.Paused {
		state = stagekit.State(task.PreviousState)
	}
	if state == stagekit.Preparing || state == stagekit.Planning || state == stagekit.AwaitingApproval {
		return "planner", latest, nil
	}
	if _, pipeline, pipelineErr := config.TaskPipeline(s.deps.Config, s.deps.ConfigPath, task.ConfigSnapshot, task.Pipeline); pipelineErr == nil {
		if task.ActiveStage != "" {
			if stage, _, ok := stageDefinition(pipeline, task.ActiveStage); ok && stage.Agent != "" {
				return stage.ID, latest, nil
			}
		}
		if state == stagekit.Checking || state == stagekit.Reviewing {
			for _, v := range slices.Backward(pipeline.Stages) {
				if v.Kind == "review" {
					return v.ID, latest, nil
				}
			}
			for _, v := range slices.Backward(pipeline.Stages) {
				if v.Kind == "build" {
					return v.ID, latest, nil
				}
			}
		}
		if state == stagekit.Completed {
			for _, stage := range pipeline.Stages {
				if stage.Kind == "build" {
					return stage.ID, latest, nil
				}
			}
		}
	}
	switch state {
	case stagekit.Preparing, stagekit.Planning, stagekit.AwaitingApproval:
		return "planner", latest, nil
	case stagekit.Building:
		return "builder", latest, nil
	case stagekit.Checking, stagekit.Reviewing:
		return "reviewer", latest, nil
	case stagekit.Completed:
		return "builder", latest, nil
	case stagekit.Blocked:
		if latest != nil && latest.Kind == "agent" {
			return latest.Owner, latest, nil
		}
		return "builder", latest, nil
	default:
		return "", latest, store.ErrConflict
	}
}

func stageDefinition(pipeline config.Pipeline, id string) (config.Stage, int, bool) {
	for index, stage := range pipeline.Stages {
		if stage.ID == id {
			return stage, index, true
		}
	}
	return config.Stage{}, -1, false
}

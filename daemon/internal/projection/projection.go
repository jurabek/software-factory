// Package projection owns read models over task state. It produces the stage
// projection the API returns without involving the orchestrator or any stage.
package projection

import (
	"context"
	"slices"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Deps are the collaborators a projection service needs.
type Deps struct {
	Store      *store.DB
	Config     config.Config
	ConfigPath string
}

// Service builds task read models.
type Service struct {
	deps Deps
}

// New constructs a projection service.
func New(deps Deps) *Service {
	return &Service{deps: deps}
}

// StageProjection projects a task's pipeline stages from its phase history.
func (s *Service) StageProjection(ctx context.Context, task store.Task) ([]store.StageProjection, error) {
	_, pipeline, err := config.TaskPipeline(s.deps.Config, s.deps.ConfigPath, task.ConfigSnapshot, task.Pipeline)
	if err != nil {
		return nil, err
	}
	if len(pipeline.Stages) == 0 {
		return []store.StageProjection{}, nil
	}
	phases, err := s.deps.Store.Phases(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	result := make([]store.StageProjection, 0, len(pipeline.Stages))
	for _, stage := range pipeline.Stages {
		value := store.StageProjection{ID: stage.ID, Kind: stage.Kind, Agent: stage.Agent, Status: "not_started"}
		phaseName := stage.ID
		if stage.Kind == "plan" {
			phaseName = "planning"
		}
		for _, phase := range slices.Backward(phases) {

			if phase.Name != phaseName || phase.Superseded {
				continue
			}
			value.AttemptID = phase.ID
			if phase.Status == "running" || phase.Status == "queued" {
				value.Status = "running"
			} else if phase.Status == "failed" {
				value.Status = "failed"
				if task.ActiveStage == stage.ID {
					value.BlockingReason = task.Error
				}
			} else if phase.Status == "success" {
				value.Status = "completed"
			}
			break
		}
		if task.ActiveStage == stage.ID {
			switch stagekit.State(task.State) {
			case stagekit.Paused:
				value.Status = "paused"
			case stagekit.Aborted:
				value.Status = "aborted"
			case stagekit.Blocked:
				if value.Status != "failed" {
					value.Status = "blocked"
					value.BlockingReason = task.Error
				}
			}
		}
		result = append(result, value)
	}
	return result, nil
}

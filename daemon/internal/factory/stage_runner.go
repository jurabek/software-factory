package factory

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// StageInput is the immutable handoff resolved by the orchestrator before a
// runner starts work. Runners do not query an arbitrary latest result.
type StageInput struct {
	Task        store.Task
	Stage       config.Stage
	Attempt     store.Phase
	Plan        string
	Checks      []store.Check
	Comparisons []store.Comparison
}

// StageOutput contains only stage-owned evidence. Task progression is kept in
// the orchestrator and is never selected by a runner.
type StageOutput struct {
	Payload string
	Checks  []store.Check
}

type stageRunner interface {
	Run(context.Context, StageInput) (StageOutput, error)
}

func (s *Service) runner(stage config.Stage) stageRunner {
	switch stage.Kind {
	case "build":
		return builderRunner{service: s}
	case "verify":
		return verifierRunner{service: s}
	case "review":
		return reviewerRunner{service: s}
	default:
		return nil
	}
}

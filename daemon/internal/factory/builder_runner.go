package factory

import "context"

type builderRunner struct{ service *Service }

func (r builderRunner) Run(ctx context.Context, input StageInput) (StageOutput, error) {
	if err := r.service.executeBuild(ctx, input.Task, input.Stage, input.Attempt); err != nil {
		return StageOutput{}, err
	}
	return StageOutput{}, nil
}

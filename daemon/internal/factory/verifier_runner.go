package factory

import "context"

type verifierRunner struct{ service *Service }

func (r verifierRunner) Run(ctx context.Context, input StageInput) (StageOutput, error) {
	if err := r.service.executeVerify(ctx, input.Task, input.Attempt); err != nil {
		return StageOutput{}, err
	}
	return StageOutput{}, nil
}

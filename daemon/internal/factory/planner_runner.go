package factory

import "context"

type plannerRunner struct{ service *Service }

// Run executes planning without applying Task progression. The caller owns
// the transition into approval, just like all other stage runners.
func (r plannerRunner) Run(ctx context.Context, input StageInput) (StageOutput, error) {
	if err := r.service.plan(ctx, input.Task, nil); err != nil {
		return StageOutput{}, err
	}
	return StageOutput{}, nil
}

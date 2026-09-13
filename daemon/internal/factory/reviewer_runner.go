package factory

import "context"

type reviewerRunner struct{ service *Service }

func (r reviewerRunner) Run(ctx context.Context, input StageInput) (StageOutput, error) {
	if err := r.service.executeReview(ctx, input.Task, input.Stage, input.Attempt); err != nil {
		return StageOutput{}, err
	}
	return StageOutput{}, nil
}

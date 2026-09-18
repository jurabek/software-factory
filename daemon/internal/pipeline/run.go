// Package pipeline owns the factory's fixed workflow: stage order, typed
// handoffs, and progression outcome. It is pure: no store, lock, checkpoint,
// or state transitions. Each stage returns a result and owns its own lifecycle;
// Pipeline reads the result flags and reports what happened.
package pipeline

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/stage"
)

// Planner, Builder, Verifier, and Reviewer are the small interfaces Pipeline
// consumes. Each concrete stage package exposes a structurally identical
// Service interface and hides its lifecycle.
type Planner interface {
	Plan(context.Context, stage.Input) (stage.PlanResult, error)
}

// Creation performs the internal setup required before a configured stage may
// inspect the repository. It is deliberately absent from pipeline projection.
type Creation interface {
	Prepare(context.Context, stage.Input) error
}

type Builder interface {
	Build(context.Context, stage.Input, stage.PlanResult) (stage.BuildResult, error)
}

type Verifier interface {
	Verify(context.Context, stage.Input, stage.PlanResult, stage.BuildResult) (stage.VerificationResult, error)
}

type Reviewer interface {
	Review(context.Context, stage.Input, stage.PlanResult, stage.BuildResult, stage.VerificationResult) (stage.ReviewResult, error)
}

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeWaiting   Outcome = "waiting_for_approval"
	OutcomeBlocked   Outcome = "blocked"
)

type Result struct {
	Outcome      Outcome
	Plan         stage.PlanResult
	Build        stage.BuildResult
	Verification stage.VerificationResult
	Review       stage.ReviewResult
}

type Pipeline struct {
	planner  Planner
	builder  Builder
	verifier Verifier
	reviewer Reviewer
	creation Creation
}

func New(planner Planner, builder Builder, verifier Verifier, reviewer Reviewer, creation ...Creation) *Pipeline {
	p := &Pipeline{planner: planner, builder: builder, verifier: verifier, reviewer: reviewer}
	if len(creation) > 0 {
		p.creation = creation[0]
	}
	return p
}

// Run executes the fixed workflow and returns an explicit progression
// outcome. Stages resolve their own durable checkpoint and apply their own
// transitions, so re-invoking Run after an approval or retry resumes rather
// than repeats work.
//
//   - Unapproved plans yield OutcomeWaiting; approval re-invokes Run, which
//     resumes from the persisted plan without replanning.
//   - A failed verification yields OutcomeBlocked; the verifier has already
//     transitioned the task.
//   - A rejected review yields OutcomeBlocked; the reviewer has already
//     transitioned the task.
//   - Context cancellation is checked before each stage and before returning
//     success so a cancelled run cannot publish a late success.
func (p *Pipeline) Run(ctx context.Context, taskID string) (Result, error) {
	input := stage.Input{TaskID: taskID}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked}, err
	}
	if p.creation != nil {
		if err := p.creation.Prepare(ctx, input); err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{Outcome: OutcomeBlocked}, err
		}
	}
	plan, err := p.planner.Plan(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked, Plan: plan}, err
	}
	if !plan.Approved {
		return Result{Outcome: OutcomeWaiting, Plan: plan}, nil
	}
	build, err := p.builder.Build(ctx, input, plan)
	if err != nil {
		return Result{Plan: plan}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked, Plan: plan, Build: build}, err
	}
	verification, err := p.verifier.Verify(ctx, input, plan, build)
	if err != nil {
		return Result{Plan: plan, Build: build}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked, Plan: plan, Build: build, Verification: verification}, err
	}
	if !verification.Passed {
		return Result{Outcome: OutcomeBlocked, Plan: plan, Build: build, Verification: verification}, nil
	}
	review, err := p.reviewer.Review(ctx, input, plan, build, verification)
	if err != nil {
		return Result{Plan: plan, Build: build, Verification: verification}, err
	}
	if !review.Approved {
		return Result{Outcome: OutcomeBlocked, Plan: plan, Build: build, Verification: verification, Review: review}, nil
	}
	return Result{Outcome: OutcomeCompleted, Plan: plan, Build: build, Verification: verification, Review: review}, nil
}

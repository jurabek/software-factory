// Package pipeline owns the factory's fixed workflow and typed stage handoffs.
//
// Pipeline owns progression policy: stage order, the approval checkpoint,
// resume/retry position, explicit result handoffs, and upstream-invalidation
// rules. Stage packages own their prompts, validation, and execution; they
// resolve durable resume state through the Publisher bridges and publish
// atomically (drain-then-publish under the task lock) so message arrival
// during completion cannot slip past a stage's final result. Factory owns
// execution goroutines; stage methods stay synchronous.
package pipeline

import "context"

type Input struct {
	TaskID string
}

type PlanResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
	Approved   bool
}

type BuildResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
}

type VerificationResult struct {
	AttemptID  string
	SnapshotID string
	Report     string
	Passed     bool
}

type ReviewResult struct {
	Payload    string
	AttemptID  string
	SnapshotID string
	Approved   bool
}

type Planner interface {
	Plan(context.Context, Input) (PlanResult, error)
}

type Builder interface {
	Build(context.Context, Input, PlanResult) (BuildResult, error)
}

type Verifier interface {
	Verify(context.Context, Input, PlanResult, BuildResult) (VerificationResult, error)
}

type Reviewer interface {
	Review(context.Context, Input, PlanResult, BuildResult, VerificationResult) (ReviewResult, error)
}

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeWaiting   Outcome = "waiting_for_approval"
	OutcomeBlocked   Outcome = "blocked"
)

type Result struct {
	Outcome      Outcome
	Plan         PlanResult
	Build        BuildResult
	Verification VerificationResult
	Review       ReviewResult
}

type Pipeline struct {
	planner  Planner
	builder  Builder
	verifier Verifier
	reviewer Reviewer
}

func New(planner Planner, builder Builder, verifier Verifier, reviewer Reviewer) *Pipeline {
	return &Pipeline{planner: planner, builder: builder, verifier: verifier, reviewer: reviewer}
}

// Run executes the fixed workflow and returns an explicit progression
// outcome. Stage implementations resolve their durable checkpoint first
// (resume via Saved*), making repeated calls resume rather than repeat work.
//
//   - Unapproved plans yield OutcomeWaiting; approval re-invokes Run, which
//     resumes from the persisted plan without replanning.
//   - A failed verification yields OutcomeBlocked; retry re-invokes Run,
//     reusing the selected plan/build (verify-only retry).
//   - A rejected review yields OutcomeBlocked; the verdict never completes
//     the task.
//   - When an upstream result changes, downstream saved results are
//     ineligible for reuse: a changed plan requires approval again, and a
//     changed build requires fresh verification and review. Lineage is
//     enforced by attempt ordering (see EligibleAfter); stages receive the
//     exact upstream results and never query "latest" state.
//   - Context cancellation is checked before each stage and before
//     publication so a cancelled run cannot publish a late success.
func (p *Pipeline) Run(ctx context.Context, taskID string) (Result, error) {
	input := Input{TaskID: taskID}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked}, err
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
	if err := ctx.Err(); err != nil {
		return Result{Outcome: OutcomeBlocked, Plan: plan}, err
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

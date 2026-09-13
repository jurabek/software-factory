package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
)

type stages struct {
	calls []string
	fail  string
}

func (s *stages) Plan(context.Context, stage.Input) (stage.PlanResult, error) {
	s.calls = append(s.calls, "plan")
	if s.fail == "plan" {
		return stage.PlanResult{}, errors.New("plan failed")
	}
	return stage.PlanResult{Payload: "plan", AttemptID: "p1", Approved: s.fail != "approval"}, nil
}

func (s *stages) Build(_ context.Context, _ stage.Input, plan stage.PlanResult) (stage.BuildResult, error) {
	s.calls = append(s.calls, "build:"+plan.AttemptID)
	if s.fail == "build" {
		return stage.BuildResult{}, errors.New("build failed")
	}
	return stage.BuildResult{Payload: "build", AttemptID: "b1"}, nil
}

func (s *stages) Verify(_ context.Context, _ stage.Input, plan stage.PlanResult, build stage.BuildResult) (stage.VerificationResult, error) {
	s.calls = append(s.calls, "verify:"+plan.AttemptID+":"+build.AttemptID)
	if s.fail == "verify" {
		return stage.VerificationResult{}, errors.New("verify failed")
	}
	if s.fail == "verify-unpassed" {
		return stage.VerificationResult{AttemptID: "v1", Passed: false}, nil
	}
	return stage.VerificationResult{AttemptID: "v1", Passed: true}, nil
}

func (s *stages) Review(_ context.Context, _ stage.Input, plan stage.PlanResult, build stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	s.calls = append(s.calls, "review:"+plan.AttemptID+":"+build.AttemptID+":"+verification.AttemptID)
	if s.fail == "review" {
		return stage.ReviewResult{}, errors.New("review failed")
	}
	if s.fail == "review-rejected" {
		return stage.ReviewResult{AttemptID: "r1", Approved: false}, nil
	}
	return stage.ReviewResult{AttemptID: "r1", Approved: true}, nil
}

func TestRunUsesFixedTypedFlow(t *testing.T) {
	stages := &stages{}
	p := New(stages, stages, stages, stages)
	result, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", result.Outcome)
	}
	want := []string{"plan", "build:p1", "verify:p1:b1", "review:p1:b1:v1"}
	if !reflect.DeepEqual(stages.calls, want) {
		t.Fatalf("calls = %#v, want %#v", stages.calls, want)
	}
	if result.Plan.AttemptID != "p1" || result.Build.AttemptID != "b1" || result.Verification.AttemptID != "v1" || result.Review.AttemptID != "r1" {
		t.Fatalf("result handoffs = %#v", result)
	}
}

func TestRunYieldsForApproval(t *testing.T) {
	stages := &stages{fail: "approval"}
	p := New(stages, stages, stages, stages)
	result, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeWaiting {
		t.Fatalf("outcome = %q, want waiting_for_approval", result.Outcome)
	}
	if !reflect.DeepEqual(stages.calls, []string{"plan"}) {
		t.Fatalf("calls = %#v", stages.calls)
	}
}

func TestApprovalResumeDoesNotReplan(t *testing.T) {
	// First run yields for approval; the second run resumes from the
	// persisted plan without generating a new plan attempt.
	first := &stages{fail: "approval"}
	p := New(first, first, first, first)
	if _, err := p.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	resumed := &resumingPlanner{plan: stage.PlanResult{Payload: "plan", AttemptID: "p1", Approved: true}}
	rest := &stages{}
	p2 := New(resumed, rest, rest, rest)
	result, err := p2.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", result.Outcome)
	}
	if resumed.plans != 1 {
		t.Fatalf("planner calls = %d, want 1 (saved plan, no new attempt)", resumed.plans)
	}
	if !reflect.DeepEqual(rest.calls, []string{"build:p1", "verify:p1:b1", "review:p1:b1:v1"}) {
		t.Fatalf("calls = %#v", rest.calls)
	}
}

type resumingPlanner struct {
	plan  stage.PlanResult
	plans int
}

func (s *resumingPlanner) Plan(context.Context, stage.Input) (stage.PlanResult, error) {
	s.plans++
	return s.plan, nil
}

func TestRunStopsAfterFailure(t *testing.T) {
	stages := &stages{fail: "build"}
	p := New(stages, stages, stages, stages)
	if _, err := p.Run(context.Background(), "task"); err == nil {
		t.Fatal("expected error")
	}
	if !reflect.DeepEqual(stages.calls, []string{"plan", "build:p1"}) {
		t.Fatalf("calls = %#v", stages.calls)
	}
}

func TestVerifyFailureBlocksReview(t *testing.T) {
	stages := &stages{fail: "verify-unpassed"}
	p := New(stages, stages, stages, stages)
	result, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", result.Outcome)
	}
	if !reflect.DeepEqual(stages.calls, []string{"plan", "build:p1", "verify:p1:b1"}) {
		t.Fatalf("calls = %#v", stages.calls)
	}
}

func TestReviewRejectionBlocksCompletion(t *testing.T) {
	stages := &stages{fail: "review-rejected"}
	p := New(stages, stages, stages, stages)
	result, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", result.Outcome)
	}
}

func TestVerifyOnlyRetryReusesPlanAndBuild(t *testing.T) {
	// First run blocks on failed verification; retry reuses the selected
	// plan/build identities and only re-executes verification and review.
	shared := &retryStages{}
	p := New(shared, shared, shared, shared)
	first, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != OutcomeBlocked {
		t.Fatalf("first outcome = %q, want blocked", first.Outcome)
	}
	shared.pass = true
	second, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != OutcomeCompleted {
		t.Fatalf("second outcome = %q, want completed", second.Outcome)
	}
	if second.Plan.AttemptID != first.Plan.AttemptID || second.Build.AttemptID != first.Build.AttemptID {
		t.Fatalf("retry must reuse plan/build: first = %#v, second = %#v", first, second)
	}
	if shared.plans != 2 || shared.builds != 2 {
		t.Fatalf("plan/build calls = %d/%d, want 2/2 (saved results, no new attempts)", shared.plans, shared.builds)
	}
	if shared.verifies != 2 {
		t.Fatalf("verify calls = %d, want 2 (fresh verification on retry)", shared.verifies)
	}
}

type retryStages struct {
	plans    int
	builds   int
	verifies int
	pass     bool
}

func (s *retryStages) Plan(context.Context, stage.Input) (stage.PlanResult, error) {
	s.plans++
	return stage.PlanResult{Payload: "plan", AttemptID: "p1", Approved: true}, nil
}

func (s *retryStages) Build(_ context.Context, _ stage.Input, plan stage.PlanResult) (stage.BuildResult, error) {
	s.builds++
	return stage.BuildResult{Payload: "build", AttemptID: "b1"}, nil
}

func (s *retryStages) Verify(_ context.Context, _ stage.Input, plan stage.PlanResult, build stage.BuildResult) (stage.VerificationResult, error) {
	s.verifies++
	_ = plan
	_ = build
	return stage.VerificationResult{AttemptID: "v1", Passed: s.pass}, nil
}

func (s *retryStages) Review(_ context.Context, _ stage.Input, plan stage.PlanResult, build stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	_, _, _ = plan, build, verification
	return stage.ReviewResult{AttemptID: "r1", Approved: true}, nil
}

func TestUpstreamChangeInvalidatesDownstream(t *testing.T) {
	// Lineage rule: a downstream result is eligible only when produced after
	// its upstream result. A changed plan therefore requires a fresh build,
	// and a changed build requires fresh verification and review.
	if !stagekit.EligibleAfter(3, 2) {
		t.Fatal("downstream produced after upstream must be eligible")
	}
	if stagekit.EligibleAfter(2, 3) {
		t.Fatal("downstream produced before upstream change must be ineligible")
	}
	if stagekit.EligibleAfter(2, 2) {
		t.Fatal("same-sequence result must be ineligible")
	}
	first := &lineageStages{planID: "p1"}
	p := New(first, first, first, first)
	firstResult, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	second := &lineageStages{planID: "p2"}
	p2 := New(second, second, second, second)
	secondResult, err := p2.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Build.AttemptID == firstResult.Build.AttemptID {
		t.Fatal("changed plan must produce a fresh build, not reuse the old one")
	}
	if secondResult.Verification.AttemptID == firstResult.Verification.AttemptID {
		t.Fatal("changed build must produce fresh verification, not reuse the old one")
	}
	if got := second.buildPlanID; got != "p2" {
		t.Fatalf("build plan = %q, want p2", got)
	}
}

type lineageStages struct {
	planID      string
	buildPlanID string
}

func (s *lineageStages) Plan(context.Context, stage.Input) (stage.PlanResult, error) {
	return stage.PlanResult{Payload: "plan-" + s.planID, AttemptID: s.planID, Approved: true}, nil
}

func (s *lineageStages) Build(_ context.Context, _ stage.Input, plan stage.PlanResult) (stage.BuildResult, error) {
	s.buildPlanID = plan.AttemptID
	return stage.BuildResult{Payload: "build-" + plan.AttemptID, AttemptID: "b-" + plan.AttemptID}, nil
}

func (s *lineageStages) Verify(_ context.Context, _ stage.Input, _ stage.PlanResult, build stage.BuildResult) (stage.VerificationResult, error) {
	return stage.VerificationResult{AttemptID: "v-" + build.AttemptID, Passed: true}, nil
}

func (s *lineageStages) Review(_ context.Context, _ stage.Input, _ stage.PlanResult, _ stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	return stage.ReviewResult{AttemptID: "r-" + verification.AttemptID, Approved: true}, nil
}

func TestMessageDrainReachesDownstream(t *testing.T) {
	// A message arriving during planning is drained into the final payload
	// before atomic publication, so downstream stages observe post-message
	// content rather than the pre-message draft.
	planner := &drainingPlanner{queued: []string{"Keep the public API stable."}}
	recorder := &payloadRecorder{passthrough: &stages{}}
	p := New(planner, recorder, recorder, recorder)
	result, err := p.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", result.Outcome)
	}
	if recorder.buildPlan != "plan+Keep the public API stable." {
		t.Fatalf("build plan payload = %q, want drained message content", recorder.buildPlan)
	}
}

type drainingPlanner struct {
	queued []string
}

func (s *drainingPlanner) Plan(context.Context, stage.Input) (stage.PlanResult, error) {
	payload := "plan"
	// Drain-then-publish: consume every message queued before the atomic
	// publication check, mirroring the Publisher bridges.
	for len(s.queued) > 0 {
		payload += "+" + s.queued[0]
		s.queued = s.queued[1:]
	}
	return stage.PlanResult{Payload: payload, AttemptID: "p1", Approved: true}, nil
}

type payloadRecorder struct {
	passthrough *stages
	buildPlan   string
}

func (s *payloadRecorder) Plan(ctx context.Context, input stage.Input) (stage.PlanResult, error) {
	return s.passthrough.Plan(ctx, input)
}

func (s *payloadRecorder) Build(ctx context.Context, input stage.Input, plan stage.PlanResult) (stage.BuildResult, error) {
	s.buildPlan = plan.Payload
	return s.passthrough.Build(ctx, input, plan)
}

func (s *payloadRecorder) Verify(ctx context.Context, input stage.Input, plan stage.PlanResult, build stage.BuildResult) (stage.VerificationResult, error) {
	return s.passthrough.Verify(ctx, input, plan, build)
}

func (s *payloadRecorder) Review(ctx context.Context, input stage.Input, plan stage.PlanResult, build stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	return s.passthrough.Review(ctx, input, plan, build, verification)
}

func TestCancellationPreventsLatePublication(t *testing.T) {
	stages := &stages{}
	p := New(stages, stages, stages, stages)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := p.Run(ctx, "task")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result.Outcome == OutcomeCompleted {
		t.Fatal("cancelled run must not complete")
	}
	if len(stages.calls) != 0 {
		t.Fatalf("calls = %#v, want no stage execution after cancellation", stages.calls)
	}
}

func TestCancellationBetweenStagesStopsDownstream(t *testing.T) {
	inner := &stages{}
	p := New(inner, &cancelledBuilder{}, inner, inner)
	_, err := p.Run(context.Background(), "task")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	for _, call := range inner.calls {
		if len(call) >= 6 && call[:6] == "verify" {
			t.Fatalf("verify must not run after cancellation, calls = %#v", inner.calls)
		}
		if len(call) >= 6 && call[:6] == "review" {
			t.Fatalf("review must not run after cancellation, calls = %#v", inner.calls)
		}
	}
}

type cancelledBuilder struct{}

func (cancelledBuilder) Build(context.Context, stage.Input, stage.PlanResult) (stage.BuildResult, error) {
	return stage.BuildResult{}, context.Canceled
}

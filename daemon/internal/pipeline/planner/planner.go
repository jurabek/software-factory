// Package planner owns the planning stage: its prompts, plan validation, the
// Preparation -> Planning -> AwaitingApproval lifecycle, and durable resume.
package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
)

type PlanStep struct {
	ID                 string   `json:"id"`
	Description        string   `json:"description"`
	ExpectedFiles      []string `json:"expected_files"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

type Result struct {
	stage.Common
	Steps     []PlanStep `json:"steps"`
	Questions []string   `json:"questions"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, stage.CommonFields...), "steps", "questions")
	if err := stage.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := stage.ValidateCommon(value.Common); err != nil {
		return value, err
	}
	if len(value.Steps) == 0 {
		return value, fmt.Errorf("planner steps are required")
	}
	if value.Questions == nil {
		return value, fmt.Errorf("planner questions array is required")
	}
	for _, question := range value.Questions {
		if strings.TrimSpace(question) == "" {
			return value, fmt.Errorf("planner questions cannot contain blank entries")
		}
	}
	seen := map[string]bool{}
	for _, step := range value.Steps {
		if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Description) == "" || step.ExpectedFiles == nil || step.AcceptanceCriteria == nil || seen[step.ID] {
			return value, fmt.Errorf("invalid or duplicate plan step")
		}
		seen[step.ID] = true
	}
	return value, nil
}

func Instructions() string {
	return `Return exactly one JSON object: {` + stage.CommonInstructions() + `,"steps":[{"id":"...","description":"...","expected_files":[],"acceptance_criteria":[]}],"questions":[]}. Put the human-readable report in report_markdown.`
}

type eventPublisher interface {
	Publish(context.Context, string, string) error
}

type service struct {
	kit    *stagekit.Kit
	tasks  *task.Service
	events eventPublisher
}

// New constructs the planning stage.
func New(kit *stagekit.Kit, tasks *task.Service, events eventPublisher) service {
	return service{kit: kit, tasks: tasks, events: events}
}

// Plan resumes a durable plan when present, otherwise renders planning prompts,
// runs the planning turn, and publishes the plan for approval.
func (s service) Plan(ctx context.Context, input stage.Input) (stage.PlanResult, error) {
	return s.kit.RunAgentStage(ctx, input.TaskID, stagekit.AgentStage[stage.PlanResult]{
		Entry: stagekit.Entry{Kind: "plan", Enter: stagekit.Planning, EnterFrom: stagekit.Preparing},
		Role:  "planner", ReadOnly: true, Instructions: Instructions(),
		Data: func(_ context.Context, task store.Task) (map[string]any, error) {
			return map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}, nil
		},
		Validate: func(_ store.Task, text string) (any, error) { return Validate(text) },
		Finalize: func(_ context.Context, _ store.Task, _ store.Phase, turn harness.TurnResult) (stagekit.Completion, error) {
			return stagekit.Completion{To: stagekit.AwaitingApproval, Status: "success", Approval: stagekit.PlanApprovalDigest(turn.Payload)}, nil
		},
		Result: func(task store.Task, phase store.Phase, payload string) (stage.PlanResult, error) {
			return stage.PlanResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: task.ApprovalActor != ""}, nil
		},
	})
}

func (s service) Approve(ctx context.Context, taskID, actor, expectedDigest string) error {
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return fmt.Errorf("plan_digest is required")
	}
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.State != stagekit.AwaitingApproval {
		return store.ErrConflict
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return err
	}
	payload, err := s.kit.ValidEnvelope(ctx, taskID, stageDef.ID)
	if err != nil {
		return err
	}
	plan, err := Validate(payload)
	if err != nil || len(plan.Questions) > 0 {
		return store.ErrConflict
	}
	currentDigest := stagekit.PlanApprovalDigest(payload)
	if expectedDigest != currentDigest {
		return ErrStalePlan
	}
	if err = s.kit.ApprovePlan(ctx, taskID, currentDigest, actor); err != nil {
		return err
	}
	return s.events.Publish(ctx, taskID, store.TaskApproved)
}

var ErrStalePlan = errors.New("plan digest is stale")

// Package planner owns the planning stage: its prompts, plan validation, the
// Preparation -> Planning -> AwaitingApproval lifecycle, and durable resume.
package planner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
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

// Service is the planning stage's public surface. Lifecycle, resume, and
// state transitions are hidden inside the package.
type Service interface {
	Plan(context.Context, stage.Input) (stage.PlanResult, error)
	Approve(context.Context, string, string, string) error
}

type service struct {
	kit    *stagekit.Kit
	events *orchestrator.Events
}

// New constructs the planning stage.
func New(kit *stagekit.Kit, events *orchestrator.Events) Service {
	return service{kit: kit, events: events}
}

// Plan resumes a durable plan when present, otherwise renders planning prompts,
// runs the planning turn, and publishes the plan for approval.
func (s service) Plan(ctx context.Context, input stage.Input) (stage.PlanResult, error) {
	if result, ok, err := s.savedPlan(ctx, input.TaskID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.beginPlan(ctx, input.TaskID)
	if err != nil {
		return stage.PlanResult{}, err
	}
	configured, err := s.kit.TaskConfig(ctx, task)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	agent, ok := configured.Config.Agent(phase.Owner)
	if !ok {
		err = fmt.Errorf("agent %s not configured", phase.Owner)
		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}
	systemPrompt, userPrompt, err := stagekit.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		Instructions(),
	)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := s.kit.AgentExec()
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	turn, err := harness.RunTurn(ctx, turner, harness.TurnInput{
		TaskID: task.ID, RequestID: stagekit.RandomID(), Phase: phase, Role: phase.Owner,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath:     task.RepositoryPath,
		SessionDir:   filepath.Join(configured.TaskDir, "sessions", phase.Name, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: true, EnvelopeKind: "planner", CorrectionSuffix: Instructions(),
		Validate: func(text string) (any, error) { return Validate(text) },
		Sink:     s.kit.Sink(task.ID, phase.ID, harnessName),
	})
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	return s.publishPlan(ctx, task, phase, turn)
}

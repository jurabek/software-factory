// Package planner owns planning-stage entry and result validation.
package planner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type PlanStep struct {
	ID                 string   `json:"id"`
	Description        string   `json:"description"`
	ExpectedFiles      []string `json:"expected_files"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

type Result struct {
	agentexec.Common
	Steps     []PlanStep `json:"steps"`
	Questions []string   `json:"questions"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, agentexec.CommonFields...), "steps", "questions")
	if err := agentexec.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := agentexec.ValidateCommon(value.Common); err != nil {
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
	return `Return exactly one JSON object: {` + agentexec.CommonInstructions() + `,"steps":[{"id":"...","description":"...","expected_files":[],"acceptance_criteria":[]}],"questions":[]}. Put the human-readable report in report_markdown.`
}

// Publisher is the narrow plan-scoped bridge into orchestration. It begins
// the planning phase, publishes the final payload atomically with message
// synchronization, and resolves durable resume state. Checkpoint mechanics
// move to Pipeline later; the bridge stays plan-scoped.
type Publisher interface {
	SavedPlan(ctx context.Context, taskID string) (pipeline.PlanResult, bool, error)
	BeginPlan(ctx context.Context, taskID string) (store.Task, store.Phase, error)
	PublishPlan(ctx context.Context, task store.Task, phase store.Phase, payload string) (pipeline.PlanResult, error)
	FailPlan(ctx context.Context, phase store.Phase, cause error)
}

// Deps supplies prompt configuration, turn execution, and plan publication.
type Deps struct {
	Turner     agentexec.Deps
	Sinks      agentexec.SinkFactory
	Configurer agentexec.Configurer
	Publisher  Publisher
}

type Service struct{ deps Deps }

func New(deps Deps) Service { return Service{deps: deps} }

// Plan resumes a durable result when present, otherwise renders planning
// prompts, runs the planning turn, and publishes the payload.
func (s Service) Plan(ctx context.Context, input pipeline.Input) (pipeline.PlanResult, error) {
	if result, ok, err := s.deps.Publisher.SavedPlan(ctx, input.TaskID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.deps.Publisher.BeginPlan(ctx, input.TaskID)
	if err != nil {
		return pipeline.PlanResult{}, err
	}
	configured, err := s.deps.Configurer.TaskConfig(ctx, task)
	if err != nil {
		s.deps.Publisher.FailPlan(ctx, phase, err)
		return pipeline.PlanResult{}, err
	}
	agent, ok := configured.Config.Agent("planner")
	if !ok {
		err = fmt.Errorf("agent planner not configured")
		s.deps.Publisher.FailPlan(ctx, phase, err)
		return pipeline.PlanResult{}, err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}
	systemPrompt, userPrompt, err := agentexec.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		filepath.Join(configured.TaskDir, "prompts", agent.Name),
		Instructions(),
	)
	if err != nil {
		s.deps.Publisher.FailPlan(ctx, phase, err)
		return pipeline.PlanResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := s.deps.Turner
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	payload, err := agentexec.RunTurn(ctx, turner, agentexec.TurnInput{
		TaskID: task.ID, Phase: phase, Role: "planner",
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath: task.RepositoryPath,
		SessionDir: filepath.Join(configured.TaskDir, "sessions", "planner", harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: true, EnvelopeKind: "planner", CorrectionSuffix: Instructions(),
		Validate: func(text string) (any, error) { return Validate(text) },
		Sink:     s.deps.Sinks(task.ID, phase.ID, harnessName),
	})
	if err != nil {
		s.deps.Publisher.FailPlan(ctx, phase, err)
		return pipeline.PlanResult{}, err
	}
	return s.deps.Publisher.PublishPlan(ctx, task, phase, payload)
}

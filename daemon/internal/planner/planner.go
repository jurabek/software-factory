// Package planner owns the planning stage: its prompts, plan validation, the
// Preparation -> Planning -> AwaitingApproval lifecycle, and durable resume.
package planner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
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

func (s service) Approve(ctx context.Context, taskID, actor, expectedDigest string) error {
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest ==
		"" {
		return fmt.Errorf("plan_digest is required")
	}
	task, err :=
		s.kit.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.State !=
		string(stagekit.AwaitingApproval) {
		return store.ErrConflict
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return err
	}
	payload, err := s.kit.
		DB().ValidEnvelope(ctx, taskID, stageDef.ID)
	if err != nil {
		return err
	}
	plan, err := Validate(payload)
	if err !=
		nil || len(plan.Questions) > 0 {
		return store.ErrConflict
	}
	currentDigest :=
		stagekit.
			PlanApprovalDigest(payload)
	if expectedDigest !=
		currentDigest {
		return ErrStalePlan
	}
	event := store.Event{ID: stagekit.RandomID(), TaskID: taskID, Kind: session.KindCustom,

		Name: "task_approved", Payload: session.CustomPayload{CustomType: "task_approved",
			Data: session.
				BoundedJSON(map[string]any{"task_id": taskID, "plan_digest": currentDigest, "actor": actor})}, Display: session.
			Display{Role: "system",
			Status: "success", Title: "Plan approved"}, AvailableActions: []string{"pause", "abort"}, StartedAt: time.Now().UTC()}
	if err = s.kit.DB().SetApproval(ctx, taskID, currentDigest, actor); err != nil {
		return err
	}
	if _, err = s.kit.DB().AppendEvent(ctx,
		s.kit.
			TaskDir(taskID), event); err != nil {
		return err
	}
	return s.events.Publish(ctx, taskID, store.
		TaskApproved)
}

func (s service) savedPlan(ctx context.Context, taskID string) (stage.PlanResult, bool, error) {
	task, err :=
		s.
			kit.
			Task(ctx, taskID)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	queued, err :=
		s.kit.DB().QueuedMessageForStages(ctx, taskID, stageDef.ID, stageDef.Agent)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	if queued {
		return stage.PlanResult{}, false, nil
	}
	phase, ok, err := s.kit.
		SuccessfulPhase(ctx, taskID, stageDef.ID)
	if err != nil ||
		!ok {
		return stage.PlanResult{}, false, err
	}
	payload, err := s.
		kit.PhaseEnvelope(ctx, taskID,

		phase.ID)
	if err != nil {
		return stage.PlanResult{}, false, err
	}
	return stage.
		PlanResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.
		OutputSnapshot,
		Approved: task.
			ApprovalActor != ""}, true, nil
}

func (s service) beginPlan(ctx context.Context, taskID string) (store.Task, store.Phase, error) {
	task, err := s.kit.Task(ctx, taskID)
	if err !=

		nil {
		return store.Task{}, store.Phase{}, err
	}
	if task.State == string(stagekit.Preparing) {
		if err = s.kit.Transition(ctx,
			task, stagekit.Planning, ""); err != nil {
			return store.Task{}, store.Phase{}, err
		}
		task.State = string(stagekit.Planning)
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return store.Task{}, store.Phase{}, err
	}
	if err = s.kit.SetActiveStage(ctx, task.ID, stageDef.
		ID); err != nil {
		return store.
			Task{}, store.Phase{}, err
	}
	phase,
		err := s.kit.BeginOrReusePhase(ctx, task.ID, stageDef.
		ID, stageDef.Kind, stageDef.
		Agent, "Execute "+stageDef.ID)
	if err != nil {
		return store.Task{}, store.
			Phase{}, err
	}
	return task, phase, nil
}

func (s service) publishPlan(ctx context.Context, task store.Task, phase store.Phase, turn harness.TurnResult) (stage.PlanResult, error) {
	baseline,

		err := workspace.Fingerprint(task)
	if err != nil {

		s.kit.Fail(ctx, phase, err)
		return stage.PlanResult{}, err
	}
	return s.
		kit.DeliverAndFinalize(ctx, stagekit.Delivery{Task: task, Phase: phase,
		Role: "planner", ReadOnly: true, Instructions: Instructions(), Validate: func(text string) (any, error) {
			return Validate(text)
		}}, turn,
		func(turn harness.TurnResult) (stage.PlanResult, error) {
			after, changedErr := workspace.Fingerprint(task)
			if changedErr !=
				nil {
				s.kit.Fail(
					ctx, phase, changedErr)
				return stage.PlanResult{},
					changedErr
			}
			if baseline !=
				after {
				readonlyErr := fmt.Errorf("planner modified repository")
				s.kit.Fail(ctx, phase, readonlyErr)
				return stage.PlanResult{}, readonlyErr
			}
			if completeErr := s.kit.
				Complete(ctx, stagekit.Completion{
					Phase: phase, From: stagekit.Planning, To: stagekit.AwaitingApproval,
					Status: "success", Approval: stagekit.PlanApprovalDigest(turn.Payload), Planner: true}); completeErr != nil {

				s.kit.Fail(ctx, phase,
					completeErr)
				return stage.PlanResult{}, completeErr
			}
			refreshed, err := s.kit.Task(ctx,
				task.ID)
			if err != nil {
				return stage.PlanResult{}, err
			}
			result, ok, err := s.savedPlan(ctx,
				refreshed.ID)
			if err != nil {
				return stage.PlanResult{}, err
			}
			if !ok {
				return stage.PlanResult{}, errNoDurableResult
			}
			return result, nil
		})
}

var ErrStalePlan = errors.New("plan digest is stale")
var errNoDurableResult = errors.New("planner completed without a durable result")

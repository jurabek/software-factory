// Package messaging owns task message intake: recipient resolution,
// agent-session bootstrap, message persistence, and the decision to hand the
// task back to the workflow. The orchestrator performs the actual launch.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Target accepts exactly one of event or attempt.
type Target struct {
	EventID   string `json:"event_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
}

func stageDefinition(pipeline config.Pipeline, id string) (config.Stage, int, bool) {
	for index, stage := range pipeline.Stages {
		if stage.ID == id {
			return stage, index, true
		}
	}
	return config.Stage{}, -1, false
}

// Request is the control-plane request to send a message to a task.
type Request struct {
	Text           string `json:"text"`
	Target         Target `json:"target"`
	IdempotencyKey string `json:"idempotency_key"`
}

type EventPublisher interface {
	Publish(context.Context, string, string) error
}

// Deps are the collaborators a messaging service needs.
type Deps struct {
	Store      *store.DB
	Config     config.Config
	ConfigPath string
	Harnesses  harness.Registry
	Root       string
	Events     EventPublisher
}

// Service accepts and routes task messages.
type Service struct {
	deps Deps
}

// New constructs a messaging service.
func New(deps Deps) *Service {
	return &Service{deps: deps}
}

// Send persists a message and reports whether the task should be handed back to
// the workflow. It is idempotent per idempotency key.
func (s *Service) Send(ctx context.Context, taskID, actor string, request Request) (store.Message, bool, error) {
	request.Text = strings.TrimSpace(request.Text)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.Text == "" {
		return store.Message{}, false, fmt.Errorf("text is required")
	}
	if request.IdempotencyKey == "" {
		return store.Message{}, false, fmt.Errorf("idempotency_key is required")
	}
	if existing, err := s.deps.Store.MessageByIdempotencyKey(ctx, taskID, request.IdempotencyKey); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Message{}, false, err
	}
	task, err := s.deps.Store.Task(ctx, taskID)
	if err != nil {
		return store.Message{}, false, err
	}
	if task.State == string(stagekit.Aborted) {
		return store.Message{}, false, store.ErrConflict
	}
	target := request.Target
	targetType, targetID, targetPhase, err := s.Resolve(ctx, taskID, target)
	if err != nil {
		return store.Message{}, false, err
	}
	role, phase, err := s.messageRecipient(ctx, task, targetPhase)
	if err != nil {
		return store.Message{}, false, err
	}
	agentSession, err := s.ensureAgentSession(ctx, task, role)
	if err != nil {
		return store.Message{}, false, err
	}
	if targetType == "task" {
		targetType, targetID = "", ""
	}
	value := store.Message{
		ID: stagekit.RandomID(), TaskID: taskID, Actor: actor, Text: request.Text,
		IdempotencyKey: request.IdempotencyKey, TargetType: targetType, TargetID: targetID,
		StageID: role, RecipientRole: agentSession.AgentName, AgentSessionID: agentSession.HarnessSessionID,
		DeliveryStatus: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	reopen := task.State == string(stagekit.AwaitingApproval) || task.State == string(stagekit.Blocked) || task.State == string(stagekit.Completed)
	event, err := stagekit.MessageEvent(ctx, s.deps.Store, value, phase)
	if err != nil {
		return store.Message{}, false, err
	}
	stored, created, err := s.deps.Store.AcceptMessageWithEvent(ctx, value, event, role == "planner", reopen, string(stagekit.StateForRole(role)), s.taskDir(taskID))
	if err != nil {
		return store.Message{}, false, err
	}
	if !created {
		return stored, false, nil
	}
	if s.deps.Events != nil {
		if err = s.deps.Events.Publish(ctx, taskID, store.TaskMessaged); err != nil {
			return store.Message{}, false, err
		}
	}
	return stored, true, nil
}

func (s *Service) taskDir(id string) string {
	return filepath.Join(s.deps.Root, "tasks", id)
}
func (s *Service) messageRecipient(ctx context.
	Context, task store.Task, target *store.Phase) (string,
	*store.Phase, error) {
	if target != nil && target.Kind == "agent" {
		return target.Owner, target, nil
	}
	if target != nil &&

		target.Name != "" && target.Kind != "check" && target.Kind != "git" {
		return target.Name, target, nil
	}
	if task.ActivePhase !=
		"" {
		active, err := s.deps.Store.PhaseByID(ctx, task.ID, task.ActivePhase)
		if err == nil && active.Status == "running" && active.Kind != "check" && active.Kind != "git" {
			if active.Kind == "agent" {
				return active.Owner, &active,
					nil
			}
			return active.Name, &active, nil
		}
	}
	phases, err :=
		s.deps.Store.Phases(ctx, task.ID)
	if err != nil {
		return "", nil, err
	}
	var latest *store.Phase
	if len(phases) > 0 {
		value := phases[len(phases)-1]
		latest =
			&value
	}
	state := stagekit.State(task.
		State,
	)
	if state == stagekit.Paused {
		state = stagekit.
			State(task.PreviousState)
	}
	if _, pipeline,

		pipelineErr := config.
		TaskPipeline(s.deps.Config,
			s.deps.ConfigPath, task.ConfigSnapshot,
			task.
				Pipeline,
		); pipelineErr == nil {
		if state ==
			stagekit.Preparing || state ==
			stagekit.Planning || state == stagekit.
			AwaitingApproval {
			for _, stage := range pipeline.Stages {
				if stage.Kind == "plan" {
					return stage.ID, latest, nil
				}
			}
		}
		if task.ActiveStage != "" {
			if stage, _, ok := stageDefinition(pipeline, task.ActiveStage); ok && stage.
				Agent != "" {
				return stage.ID, latest,
					nil
			}
		}
		if state == stagekit.
			Checking || state == stagekit.
			Reviewing {
			for _, v := range slices.Backward(pipeline.Stages) {
				if v.Kind == "review" {
					return v.ID, latest, nil
				}
			}
			for _, v := range slices.Backward(pipeline.
				Stages) {
				if v.Kind == "build" {
					return v.ID, latest,
						nil
				}
			}
		}
		if state == stagekit.Completed {
			for _, stage := range pipeline.
				Stages {
				if stage.
					Kind ==
					"build" {
					return stage.ID, latest, nil
				}
			}
		}
	}
	switch state {
	case stagekit.
		Preparing, stagekit.Planning,

		stagekit.
			AwaitingApproval:
		return "planner",
			latest, nil
	case stagekit.Building:
		return "builder", latest, nil
	case stagekit.Checking, stagekit.Reviewing:
		return "reviewer", latest,
			nil
	case stagekit.Completed:
		return "builder", latest, nil
	case
		stagekit.Blocked:
		if latest !=
			nil && latest.Kind == "agent" {
			return latest.Owner,
				latest, nil
		}
		return "builder", latest,
			nil
	default:
		return "", latest,
			store.ErrConflict
	}
}
func (s *Service) ensureAgentSession(ctx context.Context, task store.Task, role string) (store.AgentSession, error) {
	configured, err := config.Resolve(s.deps.Config,
		s.deps.ConfigPath, task.ConfigSnapshot)
	if err != nil {
		return store.AgentSession{}, err
	}
	agentName := role
	if _, pipeline, pipelineErr := config.TaskPipeline(s.deps.Config, s.deps.
		ConfigPath, task.ConfigSnapshot, task.Pipeline,
	); pipelineErr == nil {
		if stage,
			_,
			ok := stageDefinition(pipeline, role); ok && stage.Agent != "" {
			agentName = stage.Agent
		}
	}
	agent, ok := configured.
		Agent(agentName)
	if !ok {
		return store.AgentSession{}, fmt.Errorf("agent %s not configured", agentName)
	}
	harnessName := configured.Defaults.CodingAgent
	if _, ok = s.deps.Harnesses.Get(harnessName); !ok {
		return store.AgentSession{}, fmt.Errorf("harness %s unavailable",

			harnessName)
	}
	stored, err := s.
		deps.
		Store.
		AgentSession(ctx, task.ID, role)
	if err == nil {
		if stored.
			Harness != harnessName {
			return store.AgentSession{}, store.ErrConflict
		}
		return stored, nil
	}
	if !errors.Is(err,
		store.
			ErrNotFound) {
		return store.AgentSession{}, err
	}
	return s.deps.
		Store.ReserveAgentSession(ctx, task.ID, store.
		AgentSession{StageID: role, AgentName: agentName, Role: agentName, Harness: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color, HarnessSessionID: uuid.New().String(), SessionDirectory: filepath.Join(s.taskDir(task.
		ID), "sessions", role, harnessName)})
}

func (s *Service) Resolve(ctx context.Context,

	taskID string, target Target) (string, string, *store.Phase, error) {
	count := 0
	if target.EventID != "" {
		count++
	}
	if target.AttemptID != "" {
		count++
	}
	if count >
		1 {
		return "", "", nil, fmt.Errorf("target accepts exactly one of event_id or attempt_id")
	}
	if target.AttemptID != "" {
		phase, err := s.deps.Store.PhaseByID(ctx, taskID, target.AttemptID)
		if err != nil {
			return "", "", nil, err
		}
		return "attempt",
			phase.ID, &phase, nil
	}
	if target.EventID != "" {
		event, err := s.deps.Store.EventByID(ctx, taskID,
			target.EventID)
		if err !=
			nil {
			return "", "", nil, err
		}
		attemptID := event.AttemptID
		if attemptID ==
			"" {
			attemptID = event.PhaseID
		}
		if attemptID ==
			"" {
			return "event", event.
					ID,
				nil, nil
		}
		phase, err := s.deps.Store.PhaseByID(ctx, taskID, attemptID)
		if err != nil {
			return "event", event.ID, nil, nil
		}
		return "event", event.ID, &phase,
			nil
	}
	phases, err := s.deps.Store.Phases(
		ctx, taskID)
	if err != nil {
		return "", "", nil, err
	}
	if len(phases) == 0 {
		return "task", taskID, nil, nil
	}
	latest := phases[len(phases)-1]
	return "task", taskID, &latest, nil
}

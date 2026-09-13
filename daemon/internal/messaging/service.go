// Package messaging owns task message intake: recipient resolution,
// agent-session bootstrap, message persistence, and the decision to hand the
// task back to the workflow. The orchestrator performs the actual launch.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/intervention"
	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Request is the control-plane request to send a message to a task.
type Request struct {
	Text           string              `json:"text"`
	Target         intervention.Target `json:"target"`
	IdempotencyKey string              `json:"idempotency_key"`
}

// Deps are the collaborators a messaging service needs.
type Deps struct {
	Store         *store.DB
	Config        config.Config
	ConfigPath    string
	Harnesses     harness.Registry
	Root          string
	Interventions *intervention.Service
	Events        *orchestrator.Events
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
	targetType, targetID, targetPhase, err := s.deps.Interventions.Resolve(ctx, taskID, target)
	if err != nil {
		return store.Message{}, false, err
	}
	if request.Target.Anchor != nil && request.Target.ArtifactID == "" {
		return store.Message{}, false, fmt.Errorf("anchor requires artifact_id")
	}
	if err = s.deps.Interventions.ValidateAnchor(ctx, taskID, target); err != nil {
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
	anchor := ""
	if request.Target.Anchor != nil {
		encoded, marshalErr := json.Marshal(request.Target.Anchor)
		if marshalErr != nil {
			return store.Message{}, false, marshalErr
		}
		anchor = string(encoded)
	}
	if targetType == "task" {
		targetType, targetID = "", ""
	}
	value := store.Message{
		ID: stagekit.RandomID(), TaskID: taskID, Actor: actor, Text: request.Text,
		IdempotencyKey: request.IdempotencyKey, TargetType: targetType, TargetID: targetID,
		Anchor: anchor, StageID: role, RecipientRole: agentSession.AgentName, AgentSessionID: agentSession.HarnessSessionID,
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

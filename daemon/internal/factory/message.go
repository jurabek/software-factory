package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"uuid"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type MessageTarget struct {
	EventID    string  `json:"event_id,omitempty"`
	ArtifactID string  `json:"artifact_id,omitempty"`
	AttemptID  string  `json:"attempt_id,omitempty"`
	Anchor     *Anchor `json:"anchor,omitempty"`
}

type SendMessageRequest struct {
	Text           string        `json:"text"`
	Target         MessageTarget `json:"target,omitempty"`
	IdempotencyKey string        `json:"idempotency_key"`
}

type RetryRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type RetryResult struct {
	SourceAttemptID string `json:"source_attempt_id"`
	BranchID        string `json:"branch_id"`
	AttemptID       string `json:"attempt_id"`
	CreatedAt       string `json:"created_at"`
}

func (s *Service) SendMessage(ctx context.Context, taskID, actor string, request SendMessageRequest) (store.Message, error) {
	var message store.Message
	err := s.executions.withTask(taskID, func() error {
		var err error
		message, err = s.sendMessage(ctx, taskID, actor, request)
		return err
	})
	return message, err
}

func (s *Service) sendMessage(ctx context.Context, taskID, actor string, request SendMessageRequest) (store.Message, error) {
	request.Text = strings.TrimSpace(request.Text)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.Text == "" {
		return store.Message{}, fmt.Errorf("text is required")
	}
	if request.IdempotencyKey == "" {
		return store.Message{}, fmt.Errorf("idempotency_key is required")
	}
	if existing, err := s.db.MessageByIdempotencyKey(ctx, taskID, request.IdempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Message{}, err
	}
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Message{}, err
	}
	if task.State == string(Aborted) {
		return store.Message{}, store.ErrConflict
	}
	target := request.Target
	targetType, targetID, targetPhase, err := s.resolveTarget(ctx, taskID, target)
	if err != nil {
		return store.Message{}, err
	}
	if request.Target.Anchor != nil && request.Target.ArtifactID == "" {
		return store.Message{}, fmt.Errorf("anchor requires artifact_id")
	}
	if err = s.validateAnchor(ctx, taskID, target); err != nil {
		return store.Message{}, err
	}
	role, phase, err := s.messageRecipient(ctx, task, targetPhase)
	if err != nil {
		return store.Message{}, err
	}
	agentSession, err := s.ensureAgentSession(ctx, task, role)
	if err != nil {
		return store.Message{}, err
	}
	anchor := ""
	if request.Target.Anchor != nil {
		encoded, marshalErr := json.Marshal(request.Target.Anchor)
		if marshalErr != nil {
			return store.Message{}, marshalErr
		}
		anchor = string(encoded)
	}
	if targetType == "task" {
		targetType, targetID = "", ""
	}
	value := store.Message{
		ID: randomID(), TaskID: taskID, Actor: actor, Text: request.Text,
		IdempotencyKey: request.IdempotencyKey, TargetType: targetType, TargetID: targetID,
		Anchor: anchor, StageID: role, RecipientRole: agentSession.AgentName, AgentSessionID: agentSession.HarnessSessionID,
		DeliveryStatus: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	toState := scheduledMessageState(task.State, role)
	eventState := task.State
	if toState != "" {
		eventState = toState
	}
	entry := session.NewTaskMessage(session.TaskMessagePayload{
		MessageID: value.ID, TaskID: value.TaskID, Text: value.Text, RecipientRole: value.RecipientRole,
		AgentSessionID: value.AgentSessionID, TargetType: value.TargetType, TargetID: value.TargetID,
		Anchor: value.Anchor, DeliveryStatus: value.DeliveryStatus,
	})
	stored, created, err := s.db.CommitMessageAcceptance(ctx, value, task.State, toState, role == "planner", store.Event{
		ID: randomID(), TaskID: taskID, PhaseID: phaseID(phase), AttemptID: phaseID(phase), BranchID: branchID(phase),
		Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display,
		AvailableActions: AvailableActions(phase, eventState), StartedAt: time.Now().UTC(),
	}, s.taskDir(taskID))
	if err != nil {
		return store.Message{}, err
	}
	if !created {
		return stored, nil
	}
	if toState != "" {
		s.launchMessageContinuation(taskID, role)
	}
	return stored, nil
}

func (s *Service) messageRecipient(ctx context.Context, task store.Task, target *store.Phase) (string, *store.Phase, error) {
	if target != nil && target.Kind == "agent" {
		return target.Owner, target, nil
	}
	if target != nil && target.Name != "" && target.Kind != "check" && target.Kind != "git" {
		return target.Name, target, nil
	}
	if task.ActivePhase != "" {
		active, err := s.db.PhaseByID(ctx, task.ID, task.ActivePhase)
		if err == nil && active.Status == "running" && active.Kind != "check" && active.Kind != "git" {
			if active.Kind == "agent" {
				return active.Owner, &active, nil
			}
			return active.Name, &active, nil
		}
	}
	phases, err := s.db.Phases(ctx, task.ID)
	if err != nil {
		return "", nil, err
	}
	var latest *store.Phase
	if len(phases) > 0 {
		value := phases[len(phases)-1]
		latest = &value
	}
	state := State(task.State)
	if state == Paused {
		state = State(task.PreviousState)
	}
	if state == Preparing || state == Planning || state == AwaitingApproval {
		return "planner", latest, nil
	}
	if _, pipeline, pipelineErr := s.taskPipeline(task); pipelineErr == nil {
		if task.ActiveStage != "" {
			if stage, _, ok := stageDefinition(pipeline, task.ActiveStage); ok && stage.Agent != "" {
				return stage.ID, latest, nil
			}
		}
		if state == Checking || state == Reviewing {
			for index := len(pipeline.Stages) - 1; index >= 0; index-- {
				if pipeline.Stages[index].Kind == "review" {
					return pipeline.Stages[index].ID, latest, nil
				}
			}
			for index := len(pipeline.Stages) - 1; index >= 0; index-- {
				if pipeline.Stages[index].Kind == "build" {
					return pipeline.Stages[index].ID, latest, nil
				}
			}
		}
		if state == Completed {
			for _, stage := range pipeline.Stages {
				if stage.Kind == "build" {
					return stage.ID, latest, nil
				}
			}
		}
	}
	switch state {
	case Preparing, Planning, AwaitingApproval:
		return "planner", latest, nil
	case Building:
		return "builder", latest, nil
	case Checking, Reviewing:
		return "reviewer", latest, nil
	case Completed:
		return "builder", latest, nil
	case Blocked:
		if latest != nil && latest.Kind == "agent" {
			return latest.Owner, latest, nil
		}
		return "builder", latest, nil
	default:
		return "", latest, store.ErrConflict
	}
}

func (s *Service) ensureAgentSession(ctx context.Context, task store.Task, role string) (store.AgentSession, error) {
	configured, err := s.taskConfig(task)
	if err != nil {
		return store.AgentSession{}, err
	}
	agentName := role
	if _, pipeline, pipelineErr := s.taskPipeline(task); pipelineErr == nil {
		if stage, _, ok := stageDefinition(pipeline, role); ok && stage.Agent != "" {
			agentName = stage.Agent
		}
	}
	agent, ok := agentForRole(configured, agentName)
	if !ok {
		return store.AgentSession{}, fmt.Errorf("agent %s not configured", agentName)
	}
	harnessName := configured.Defaults.CodingAgent
	if _, ok = s.harnesses.Get(harnessName); !ok {
		return store.AgentSession{}, fmt.Errorf("harness %s unavailable", harnessName)
	}
	stored, err := s.db.AgentSession(ctx, task.ID, role)
	if err == nil {
		if stored.Harness != harnessName {
			return store.AgentSession{}, store.ErrConflict
		}
		return stored, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.AgentSession{}, err
	}
	return s.db.ReserveAgentSession(ctx, task.ID, store.AgentSession{
		StageID: role, AgentName: agentName, Role: agentName, Harness: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		HarnessSessionID: uuid.New().String(), SessionDirectory: filepath.Join(s.taskDir(task.ID), "sessions", role, harnessName), AccountingComplete: true,
	})
}

func (s *Service) scheduleMessage(ctx context.Context, task store.Task, role string) error {
	switch State(task.State) {
	case AwaitingApproval:
		if err := s.db.ReopenTask(ctx, task.ID, string(stateForRole(role))); err != nil {
			return err
		}
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	case Blocked:
		state := stateForRole(role)
		if err := s.db.Transition(ctx, task.ID, task.State, string(state), "", ""); err != nil {
			return err
		}
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	case Completed:
		if err := s.db.ReopenTask(ctx, task.ID, string(stateForRole(role))); err != nil {
			return err
		}
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	}
	return nil
}

func scheduledMessageState(current, role string) string {
	switch State(current) {
	case AwaitingApproval, Blocked, Completed:
		return string(stateForRole(role))
	default:
		return ""
	}
}

func (s *Service) launchMessageContinuation(taskID, role string) {
	s.launch(taskID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
}

func (s *Service) kickQueuedMessage(taskID string) {
	ctx := context.Background()
	_ = s.executions.withTask(taskID, func() error {
		task, err := s.db.Task(ctx, taskID)
		if err != nil {
			return nil
		}
		if task.State != string(AwaitingApproval) && task.State != string(Blocked) && task.State != string(Completed) {
			return nil
		}
		message, err := s.db.NextQueuedTaskMessage(ctx, taskID)
		if err != nil {
			return nil
		}
		return s.scheduleMessage(ctx, task, message.RecipientRole)
	})
}

func stateForRole(role string) State {
	switch role {
	case "planner":
		return Planning
	case "reviewer":
		return Reviewing
	default:
		return Building
	}
}

func (s *Service) continueMessages(ctx context.Context, taskID, role string) error {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, taskID, string(stateForRole(role)), "agent", role, "Continue agent session")
	if err != nil {
		return err
	}
	validate := validatorForRole(role)
	var baseline map[string]string
	if isReadOnlyOwner(role) {
		baseline, err = repositoryFingerprints(ctx, s.git, task.Repositories)
		if err != nil {
			return err
		}
	}
	var profiles map[string]Materialization
	if role == "builder" {
		profiles, err = s.workspace.InspectProfiles(ctx, task)
		if err != nil {
			return err
		}
		validate = s.builderValidator(ctx, task, profiles)
	}
	next := stateAfterRole(role)
	payload, err := s.completeAgentPhase(ctx, task, phase, role, validate, "", func(payload string) error {
		if payload == "" {
			return fmt.Errorf("queued message not found for %s", role)
		}
		if role == "builder" {
			return s.validateBuilderPaths(ctx, task, profiles)
		}
		if role == "reviewer" {
			review, validateErr := ValidateReview(payload)
			if validateErr != nil {
				return validateErr
			}
			if !review.Approved {
				return fmt.Errorf("reviewer rejected implementation")
			}
		}
		if isReadOnlyOwner(role) {
			after, changedErr := repositoryFingerprints(ctx, s.git, task.Repositories)
			if changedErr != nil {
				return changedErr
			}
			if !sameFingerprints(baseline, after) {
				return fmt.Errorf("%s modified repository", role)
			}
		}
		return nil
	}, next)
	if err != nil {
		return err
	}
	if role == "builder" {
		return s.continueAfterBuilder(ctx, taskID)
	}
	_ = payload
	return nil
}

func stateAfterRole(role string) State {
	switch role {
	case "planner":
		return AwaitingApproval
	case "builder":
		return Checking
	default:
		return Completed
	}
}

func (s *Service) completeAgentPhase(ctx context.Context, task store.Task, phase store.Phase, role string, validate validator, payload string, verify func(string) error, next State) (string, error) {
	return s.completeAgentPhaseWithEnvelope(ctx, task, phase, role, validate, payload, verify, next, nil)
}

func (s *Service) completeAgentPhaseWithEnvelope(ctx context.Context, task store.Task, phase store.Phase, role string, validate validator, payload string, verify func(string) error, next State, envelope *store.Envelope) (string, error) {
	for {
		continued, err := s.drainMessages(ctx, task, phase, role, validate)
		if err != nil {
			s.failPhase(ctx, phase, err)
			return "", err
		}
		if continued != "" {
			payload = continued
			envelope = nil
		}
		var completed bool
		err = s.executions.withTask(task.ID, func() error {
			if err = s.executionGuard(ctx, task.ID); err != nil {
				return err
			}
			_, err = s.db.NextQueuedMessage(ctx, task.ID, role)
			if err == nil {
				return nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				s.failPhase(ctx, phase, err)
				return err
			}
			if err = verify(payload); err != nil {
				s.failPhase(ctx, phase, err)
				return err
			}
			if envelope != nil {
				err = s.endPhaseToState(ctx, phase, "success", nil, string(stateForRole(role)), string(next), *envelope)
			} else {
				err = s.endPhaseToState(ctx, phase, "success", nil, string(stateForRole(role)), string(next))
			}
			completed = err == nil
			return err
		})
		if !completed && err == nil {
			continue
		}
		return payload, err
	}
}

func (s *Service) validateBuilderPaths(ctx context.Context, task store.Task, profiles map[string]Materialization) error {
	for _, repository := range task.Repositories {
		files, err := factorygit.ChangedFiles(ctx, s.git, repository.WorkingPath, repositoryReviewBase(repository))
		if err != nil {
			return err
		}
		for _, file := range files {
			if factorygit.MatchesPath(file, profiles[repository.Name].Protected) {
				return fmt.Errorf("builder changed protected path %s/%s", repository.Name, file)
			}
		}
	}
	return nil
}

func validatorForRole(role string) validator {
	switch role {
	case "planner":
		return func(text string) (any, error) { return ValidatePlan(text) }
	case "builder":
		return func(text string) (any, error) { return ValidateBuild(text) }
	default:
		return func(text string) (any, error) { return ValidateReview(text) }
	}
}

func (s *Service) drainMessages(ctx context.Context, task store.Task, phase store.Phase, role string, validate validator) (string, error) {
	configured, err := s.taskConfig(task)
	if err != nil {
		return "", err
	}
	agent, ok := agentForRole(configured, role)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", role)
	}
	adapter, ok := s.harnesses.Get(configured.Defaults.CodingAgent)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", configured.Defaults.CodingAgent)
	}
	storedSession, err := s.db.AgentSession(ctx, task.ID, role)
	if err != nil {
		return "", err
	}
	var latest string
	for {
		message, nextErr := s.db.NextQueuedMessage(ctx, task.ID, role)
		if errors.Is(nextErr, store.ErrNotFound) {
			return latest, nil
		}
		if nextErr != nil {
			return "", nextErr
		}
		if message.AgentSessionID != storedSession.HarnessSessionID {
			err = fmt.Errorf("message agent session identity is stale")
			s.failMessage(ctx, message, phase, "session_unavailable")
			return "", err
		}
		systemPrompt, promptErr := s.messageSystemPrompt(ctx, message, role)
		if promptErr != nil {
			s.failMessage(ctx, message, phase, "context_unavailable")
			return "", promptErr
		}
		request := harness.Request{
			CWD: task.PrimaryRepositoryPath, Prompt: message.Text, SystemPrompt: systemPrompt,
			Model: agent.Model, Thinking: agent.Thinking, SessionID: storedSession.HarnessSessionID,
			SessionDirectory: storedSession.SessionDirectory, RawOutputPath: filepath.Join(storedSession.SessionDirectory, "raw-output.jsonl"),
			DeadlineMS: configured.Runtime.AgentDeadlineMS, Resume: true,
		}
		for _, repository := range task.Repositories {
			if !repository.Primary && repository.WorkingPath != "" {
				request.AdditionalDirectories = append(request.AdditionalDirectories, repository.WorkingPath)
			}
		}
		for correction := 0; correction <= configured.Runtime.JSONFixAttempts; correction++ {
			invocationID := uuid.New().String()
			if correction == 0 {
				if err = s.db.BeginMessageInvocation(ctx, task.ID, role, invocationID, message.ID); err != nil {
					return "", err
				}
				message.DeliveryStatus = "delivered"
				message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
				_ = s.traceMessage(ctx, message, &phase)
			} else if err = s.db.BeginAgentInvocation(ctx, task.ID, role, invocationID); err != nil {
				return "", err
			}
			var before map[string]string
			if isReadOnlyOwner(role) {
				before, err = repositoryFingerprints(ctx, s.git, task.Repositories)
				if err != nil {
					return "", err
				}
			}
			result, runErr := adapter.Run(ctx, request, s.eventSink(task.ID, phase.ID, storedSession.Harness))
			if isReadOnlyOwner(role) {
				after, fingerprintErr := repositoryFingerprints(ctx, s.git, task.Repositories)
				if fingerprintErr != nil {
					runErr = errors.Join(runErr, fingerprintErr)
				} else if !sameFingerprints(before, after) {
					runErr = errors.Join(runErr, fmt.Errorf("%s modified repository", role))
				}
			}
			if result.SessionID == "" {
				result.SessionID = storedSession.HarnessSessionID
			}
			sessionMismatch := result.SessionID != storedSession.HarnessSessionID
			if sessionMismatch {
				runErr = errors.Join(runErr, fmt.Errorf("harness session identity changed from %s to %s", storedSession.HarnessSessionID, result.SessionID))
			}
			storedSession.SessionReady = storedSession.SessionReady || result.SessionReady
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			finalizeErr := s.db.FinalizeAgentInvocation(cleanupCtx, task.ID, role, invocationID, store.AgentSession{
				Role: role, Harness: storedSession.Harness, Provider: result.Provider, Model: result.Model, Thinking: agent.Thinking, Color: agent.Color,
				HarnessSessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, SessionReady: storedSession.SessionReady,
				NativeTranscriptPath: result.NativeTranscriptPath, ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow,
				Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost, AccountingComplete: result.AccountingComplete,
			})
			cancel()
			if finalizeErr != nil {
				s.failMessage(ctx, message, phase, "delivery_failed")
				return "", finalizeErr
			}
			if runErr != nil {
				reason := "harness_error"
				if sessionMismatch {
					reason = "session_identity_changed"
				} else if errors.Is(runErr, context.Canceled) {
					reason = "invocation_cancelled"
				}
				s.failMessage(ctx, message, phase, reason)
				return "", runErr
			}
			_, validationErr := validate(result.Text)
			if err = s.db.SaveEnvelope(ctx, randomID(), task.ID, phase.ID, role, role, result.Text, validationErr == nil, correction+1); err != nil {
				s.failMessage(ctx, message, phase, "delivery_failed")
				return "", err
			}
			if validationErr == nil {
				if role == "builder" {
					if evidenceErr := s.persistBuilderEvidence(ctx, task, phase, result.Text); evidenceErr != nil {
						s.failMessage(ctx, message, phase, "evidence_persistence_failed")
						return "", evidenceErr
					}
				}
				latest = result.Text
				break
			}
			if correction == configured.Runtime.JSONFixAttempts {
				s.failMessage(ctx, message, phase, "invalid_agent_response")
				return "", fmt.Errorf("%s envelope invalid after corrections: %w", role, validationErr)
			}
			request.Prompt = "Your previous final response was invalid: " + validationErr.Error() + "\n" + envelopeInstructions(role)
		}
	}
}

func (s *Service) messageSystemPrompt(ctx context.Context, message store.Message, role string) (string, error) {
	contextValue := map[string]any{}
	if message.Target != nil {
		contextValue["target"] = message.Target
	}
	checks, err := s.db.Checks(ctx, message.TaskID)
	if err != nil {
		return "", err
	}
	failed := make([]store.Check, 0)
	for _, check := range checks {
		if check.Status == "failed" {
			failed = append(failed, check)
		}
	}
	if len(failed) > 0 {
		contextValue["failed_checks"] = failed
	}
	if len(contextValue) == 0 {
		return envelopeInstructions(role), nil
	}
	encoded, err := json.Marshal(contextValue)
	if err != nil {
		return "", fmt.Errorf("encode message context: %w", err)
	}
	return envelopeInstructions(role) + "\n\nFactory context for this turn: " + string(encoded), nil
}

func (s *Service) failMessage(ctx context.Context, message store.Message, phase store.Phase, reason string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	failed, err := s.db.FailMessage(cleanupCtx, message.TaskID, message.ID, reason)
	if err == nil {
		_ = s.traceMessage(cleanupCtx, failed, &phase)
	}
}

// Retry remains a public API shape while the retry implementation is deferred.
func (s *Service) Retry(context.Context, string, string, RetryRequest) (RetryResult, error) {
	return RetryResult{}, fmt.Errorf("retry is not implemented")
}

func (s *Service) traceMessage(ctx context.Context, message store.Message, phase *store.Phase) error {
	phaseID, branchID := "", ""
	if phase != nil {
		phaseID, branchID = phase.ID, phase.BranchID
	}
	entry := session.NewTaskMessage(session.TaskMessagePayload{
		MessageID: message.ID, TaskID: message.TaskID, Text: message.Text, RecipientRole: message.RecipientRole,
		AgentSessionID: message.AgentSessionID, TargetType: message.TargetType, TargetID: message.TargetID,
		Anchor: message.Anchor, DeliveryStatus: message.DeliveryStatus, FailureReason: message.FailureReason,
	})
	taskState := ""
	if task, taskErr := s.db.Task(ctx, message.TaskID); taskErr == nil {
		taskState = task.State
	}
	_, err := s.db.AppendEvent(ctx, s.taskDir(message.TaskID), store.Event{
		ID: randomID(), TaskID: message.TaskID, PhaseID: phaseID, AttemptID: phaseID, BranchID: branchID,
		Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display,
		AvailableActions: AvailableActions(phase, taskState), StartedAt: time.Now().UTC(),
	})
	return err
}

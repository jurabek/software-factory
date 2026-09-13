package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/builder"
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

func (s *Service) SendMessage(ctx context.Context, taskID, actor string, request SendMessageRequest) (store.Message, error) {
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()

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
	target := InterventionTarget(request.Target)
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
	reopen := task.State == string(AwaitingApproval) || task.State == string(Blocked) || task.State == string(Completed)
	event, err := s.messageEvent(ctx, value, phase)
	if err != nil {
		return store.Message{}, err
	}
	stored, created, err := s.db.AcceptMessageWithEvent(ctx, value, event, role == "planner", reopen, string(stateForRole(role)), s.taskDir(taskID))
	if err != nil {
		return store.Message{}, err
	}
	if !created {
		return stored, nil
	}
	if err = s.scheduleMessage(ctx, task, role); err != nil {
		return store.Message{}, err
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
	if _, pipeline, pipelineErr := s.pipelines.taskPipeline(task); pipelineErr == nil {
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
	if _, pipeline, pipelineErr := s.pipelines.taskPipeline(task); pipelineErr == nil {
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
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	case Blocked:
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	case Completed:
		s.launch(task.ID, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, role) })
	}
	return nil
}

func (s *Service) kickQueuedMessage(taskID string) {
	ctx := context.Background()
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return
	}
	if task.State != string(AwaitingApproval) && task.State != string(Blocked) && task.State != string(Completed) {
		return
	}
	message, err := s.db.NextQueuedTaskMessage(ctx, taskID)
	if err != nil {
		return
	}
	_ = s.scheduleMessage(ctx, task, message.StageID)
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

func (s *Service) continueMessages(ctx context.Context, taskID, stageID string) error {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	storedSession, err := s.ensureAgentSession(ctx, task, stageID)
	if err != nil {
		return err
	}
	agentName := storedSession.AgentName
	phaseName, phaseKind := string(stateForRole(agentName)), "agent"
	if _, pipeline, pipelineErr := s.pipelines.taskPipeline(task); pipelineErr == nil {
		if stage, _, ok := stageDefinition(pipeline, stageID); ok {
			phaseName, phaseKind = stage.ID, stage.Kind
		}
	}
	phase, err := s.beginPhase(ctx, taskID, phaseName, phaseKind, agentName, "Continue agent session")
	if err != nil {
		return err
	}
	validate := validatorForRole(phaseEnvelopeKind(phase, agentName))
	var baseline string
	if phaseReadOnly(phase, agentName) {
		baseline, err = repositoryFingerprint(ctx, s.git, task)
		if err != nil {
			return err
		}
	}
	var profile Materialization
	if phase.Kind == "build" || agentName == "builder" {
		profile, err = readTaskProfile(task)
		if err != nil {
			return err
		}
		validate = s.quality.builderValidator(ctx, task, profile)
	}
	next := stateAfterRole(phaseEnvelopeKind(phase, agentName))
	payload, err := s.completeAgentPhase(ctx, task, phase, stageID, agentName, validate, "", func(payload string) error {
		if payload == "" {
			return fmt.Errorf("queued message not found for %s", stageID)
		}
		if phase.Kind == "build" || agentName == "builder" {
			return s.validateBuilderPaths(ctx, task, profile)
		}
		if phase.Kind == "review" || agentName == "reviewer" {
			review, validateErr := ValidateReview(payload)
			if validateErr != nil {
				return validateErr
			}
			if !review.Approved {
				return fmt.Errorf("reviewer rejected implementation")
			}
		}
		if phaseReadOnly(phase, agentName) {
			after, changedErr := repositoryFingerprint(ctx, s.git, task)
			if changedErr != nil {
				return changedErr
			}
			if baseline != after {
				return fmt.Errorf("%s modified repository", stageID)
			}
		}
		return nil
	}, next)
	if err != nil {
		return err
	}
	if phase.Kind == "build" || agentName == "builder" {
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

func (s *Service) completeAgentPhase(ctx context.Context, task store.Task, phase store.Phase, stageID, agentName string, validate validator, payload string, verify func(string) error, next State) (string, error) {
	for {
		continued, err := s.drainMessages(ctx, task, phase, stageID, agentName, validate)
		if err != nil {
			s.failPhase(ctx, phase, err)
			return "", err
		}
		if continued != "" {
			payload = continued
		}
		lock := s.taskLock(task.ID)
		lock.Lock()
		_, err = s.db.NextQueuedMessage(ctx, task.ID, stageID)
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			s.failPhase(ctx, phase, err)
			return "", err
		}
		if err = verify(payload); err != nil {
			s.failPhase(ctx, phase, err)
			lock.Unlock()
			return "", err
		}
		envelopeRole := phaseEnvelopeKind(phase, agentName)
		artifact, artifactErr := s.agentReportArtifact(task, phase, envelopeRole, payload)
		if artifactErr != nil {
			s.failPhase(ctx, phase, artifactErr)
			lock.Unlock()
			return "", artifactErr
		}
		if envelopeRole == "planner" {
			err = s.completePlannerPhase(ctx, phase, stateForPhase(phase), next, "success", nil, planApprovalDigest(payload, artifact.Digest), &artifact)
		} else {
			err = s.completePhaseTransitionWithArtifact(ctx, phase, stateForPhase(phase), next, "success", nil, &artifact)
		}
		if err != nil {
			s.failPhase(ctx, phase, err)
		}
		lock.Unlock()
		return payload, err
	}
}

func (s *Service) agentReportArtifact(task store.Task, phase store.Phase, role, payload string) (store.Artifact, error) {
	validated, err := validatorForRole(role)(payload)
	if err != nil {
		if strings.TrimSpace(payload) == "" {
			return store.Artifact{}, err
		}
		return s.reportArtifact(task, phase, role, payload, role), nil
	}
	report, err := reportMarkdown(validated)
	if err != nil {
		// Direct orchestration tests may supply a deliberately small custom
		// validator. Production agent validators require report_markdown.
		if strings.TrimSpace(payload) == "" {
			return store.Artifact{}, err
		}
		report = payload
	}
	return s.reportArtifact(task, phase, role, report, role), nil
}

func (s *Service) validateBuilderPaths(ctx context.Context, task store.Task, profile Materialization) error {
	return builder.CheckProtectedPaths(ctx, s.git, task.RepositoryPath, reviewBase(task), profile.Protected)
}

func validatorForRole(role string) validator {
	switch role {
	case "planner":
		return func(text string) (any, error) { return ValidatePlan(text) }
	case "builder", "build":
		return func(text string) (any, error) { return ValidateBuild(text) }
	case "reviewer", "review":
		return func(text string) (any, error) { return ValidateReview(text) }
	default:
		return func(string) (any, error) { return nil, fmt.Errorf("unsupported envelope role %q", role) }
	}
}

func (s *Service) drainMessages(ctx context.Context, task store.Task, phase store.Phase, stageID, agentName string, validate validator) (string, error) {
	configured, err := s.taskConfig(task)
	if err != nil {
		return "", err
	}
	agent, ok := agentForRole(configured, agentName)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", agentName)
	}
	adapter, ok := s.harnesses.Get(configured.Defaults.CodingAgent)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", configured.Defaults.CodingAgent)
	}
	storedSession, err := s.db.AgentSession(ctx, task.ID, stageID)
	if err != nil {
		return "", err
	}
	var latest string
	for {
		message, nextErr := s.db.NextQueuedMessage(ctx, task.ID, stageID)
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
		systemPrompt, promptErr := s.messageSystemPrompt(ctx, message, phaseEnvelopeKind(phase, agentName))
		if promptErr != nil {
			s.failMessage(ctx, message, phase, "context_unavailable")
			return "", promptErr
		}
		request := harness.Request{
			CWD: task.RepositoryPath, Prompt: message.Text, SystemPrompt: systemPrompt,
			Model: agent.Model, Thinking: agent.Thinking, SessionID: storedSession.HarnessSessionID,
			SessionDirectory: storedSession.SessionDirectory, RawOutputPath: filepath.Join(storedSession.SessionDirectory, "raw-output.jsonl"),
			DeadlineMS: configured.Runtime.AgentDeadlineMS, Resume: true,
		}
		for correction := 0; correction <= configured.Runtime.JSONFixAttempts; correction++ {
			invocationID := uuid.New().String()
			if correction == 0 {
				message.DeliveryStatus = "delivered"
				message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
				event, eventErr := s.messageEvent(ctx, message, &phase)
				if eventErr != nil {
					return "", eventErr
				}
				if err = s.db.BeginMessageInvocationWithEvent(ctx, task.ID, stageID, invocationID, message.ID, event, s.taskDir(task.ID)); err != nil {
					return "", err
				}
			} else if err = s.db.BeginAgentInvocation(ctx, task.ID, stageID, invocationID); err != nil {
				return "", err
			}
			var before string
			if phaseReadOnly(phase, agentName) {
				before, err = repositoryFingerprint(ctx, s.git, task)
				if err != nil {
					return "", err
				}
			}
			result, runErr := agentexec.Invoke(ctx, adapter, request, s.eventSink(task.ID, phase.ID, storedSession.Harness))
			if phaseReadOnly(phase, agentName) {
				after, fingerprintErr := repositoryFingerprint(ctx, s.git, task)
				if fingerprintErr != nil {
					runErr = errors.Join(runErr, fingerprintErr)
				} else if before != after {
					runErr = errors.Join(runErr, fmt.Errorf("%s modified repository", stageID))
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
			finalizeErr := s.db.FinalizeAgentInvocation(cleanupCtx, task.ID, stageID, invocationID, store.AgentSession{
				StageID: stageID, AgentName: agentName, Role: agentName, Harness: storedSession.Harness, Provider: result.Provider, Model: result.Model, Thinking: agent.Thinking, Color: agent.Color,
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
			envelopeRole := phaseEnvelopeKind(phase, agentName)
			if err = s.db.SaveEnvelope(ctx, randomID(), task.ID, phase.ID, stageID, envelopeRole, result.Text, validationErr == nil, correction+1); err != nil {
				s.failMessage(ctx, message, phase, "delivery_failed")
				return "", err
			}
			if validationErr == nil {
				if phase.Kind == "build" || agentName == "builder" {
					if evidenceErr := s.quality.persistBuilderEvidence(ctx, task, phase, result.Text); evidenceErr != nil {
						s.failMessage(ctx, message, phase, "evidence_persistence_failed")
						return "", evidenceErr
					}
				}
				latest = result.Text
				break
			}
			if correction == configured.Runtime.JSONFixAttempts {
				s.failMessage(ctx, message, phase, "invalid_agent_response")
				return "", fmt.Errorf("%s envelope invalid after corrections: %w", stageID, validationErr)
			}
			request.Prompt = "Your previous final response was invalid: " + validationErr.Error() + "\n" + envelopeInstructions(envelopeRole)
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
	message.DeliveryStatus = "failed"
	message.FailureReason = reason
	event, err := s.messageEvent(cleanupCtx, message, &phase)
	if err != nil {
		return
	}
	_, _ = s.db.FailMessageWithEvent(cleanupCtx, message.TaskID, message.ID, reason, event, s.taskDir(message.TaskID))
}

func (s *Service) Retry(ctx context.Context, taskID, attemptID string, request RetryRequest) (store.RetryResult, error) {
	lock := s.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()

	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return store.RetryResult{}, fmt.Errorf("idempotency_key is required")
	}
	if existing, err := s.db.RetryByIdempotencyKey(ctx, taskID, request.IdempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.RetryResult{}, err
	}
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.RetryResult{}, err
	}
	switch State(task.State) {
	case Preparing, Planning, Building, Checking, Reviewing:
		return store.RetryResult{}, store.ErrConflict
	}
	phase, err := s.db.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return store.RetryResult{}, err
	}
	if phase.Status == "running" || phase.Status == "queued" {
		return store.RetryResult{}, store.ErrConflict
	}
	if phase.InputSnapshot == "" {
		return store.RetryResult{}, fmt.Errorf("attempt input snapshot is required")
	}
	if task.RepositoryPath != "" {
		branchName := "software-factory/retry/" + randomID()
		if err = factorygit.RestoreForRetry(ctx, s.git, task.RepositoryType, task.CanonicalRepositoryPath, task.RepositoryPath, task.BaseSHA, branchName); err != nil {
			return store.RetryResult{}, err
		}
		if _, err = s.db.ExecContext(ctx, `update tasks set review_base_sha=?,branch_name=? where id=?`, task.ReviewBaseSHA, branchName, taskID); err != nil {
			return store.RetryResult{}, err
		}
	}
	if err = s.MaterializeSnapshot(ctx, task, phase.InputSnapshot); err != nil {
		return store.RetryResult{}, err
	}
	parentBranch := task.SelectedBranchID
	if phase.BranchID != "" {
		parentBranch = phase.BranchID
	}
	branch := store.Branch{ID: randomID(), TaskID: taskID, ParentBranchID: parentBranch, ForkAttemptID: phase.ID, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return store.RetryResult{}, err
	}
	retry := store.Phase{
		ID: randomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: phase.Name, Kind: phase.Kind, Owner: phase.Owner,
		Description: phase.Description, Status: "queued", Attempt: phase.Attempt + 1, BranchID: branch.ID,
		DefinitionID: phase.DefinitionID, InputSnapshot: phase.InputSnapshot,
	}
	result, created, err := s.db.ApplyRetry(ctx, request.IdempotencyKey, branch, retry, string(stateForPhase(phase)))
	if err != nil {
		return store.RetryResult{}, err
	}
	if created {
		s.launch(taskID, func(ctx context.Context, id string) error { return s.runRetryAttempt(ctx, id, result.AttemptID) })
	}
	return result, nil
}

func (s *Service) runRetryAttempt(ctx context.Context, taskID, attemptID string) error {
	phase, err := s.db.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return err
	}
	if err = s.db.StartQueuedPhase(ctx, taskID, attemptID); err != nil {
		return err
	}
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	_ = s.traceBranch(ctx, taskID, phase, session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, InputSnapshot: phase.InputSnapshot}))
	switch phase.Kind {
	case "agent", "build", "review":
		return s.runRetryAgent(ctx, task, phase)
	case "check", "verify":
		if err = s.executeVerify(ctx, task, phase); err != nil {
			return err
		}
		return s.progress(ctx, taskID)
	case "git":
		return s.runRetryPrepare(ctx, task, phase)
	default:
		err = fmt.Errorf("retry executor %q is unsupported", phase.Kind)
		s.failPhase(ctx, phase, err)
		return err
	}
}

func (s *Service) runRetryPrepare(ctx context.Context, task store.Task, phase store.Phase) error {
	if task.RepositoryPath == "" {
		err := fmt.Errorf("prepared repository path is unavailable")
		s.failPhase(ctx, phase, err)
		return err
	}
	if _, err := os.Stat(task.RepositoryPath); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	lock := s.taskLock(task.ID)
	lock.Lock()
	err := s.endPhase(ctx, phase, "success", nil)
	if err == nil {
		err = s.db.Transition(ctx, task.ID, string(Preparing), string(Planning), "", "")
	}
	lock.Unlock()
	if err != nil {
		return err
	}
	task, err = s.db.Task(ctx, task.ID)
	if err != nil {
		return err
	}
	return s.plan(ctx, task, nil)
}

func (s *Service) runRetryAgent(ctx context.Context, task store.Task, phase store.Phase) error {
	validate := validatorForRole(phase.Owner)
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}
	var profile Materialization
	var baseline string
	var err error
	if phase.Owner == "builder" || phase.Owner == "reviewer" {
		plan, planErr := s.db.ValidEnvelope(ctx, task.ID, "planner")
		if planErr != nil {
			return planErr
		}
		data["Plan"] = plan
	}
	if phase.Owner == "builder" {
		profile, err = readTaskProfile(task)
		if err != nil {
			return err
		}
		validate = s.quality.builderValidator(ctx, task, profile)
	}
	if phase.Owner == "reviewer" {
		baseline, err = repositoryFingerprint(ctx, s.git, task)
		if err != nil {
			return err
		}
		changedFiles, changedErr := taskChangedFiles(ctx, s.git, task, true)
		if changedErr != nil {
			return changedErr
		}
		data["ChangedFiles"] = changedFiles
		data["Checks"], err = s.db.Checks(ctx, task.ID)
		if err != nil {
			return err
		}
		data["TestChanges"], err = s.db.TestChanges(ctx, task.ID)
		if err != nil {
			return err
		}
		data["Comparisons"], err = s.db.Comparisons(ctx, task.ID)
		if err != nil {
			return err
		}
		changes, diffErr := s.tasks.diffRepository(ctx, task, true)
		if diffErr != nil {
			return diffErr
		}
		data["Diff"] = changes
	}
	if phase.Owner == "planner" {
		baseline, err = repositoryFingerprint(ctx, s.git, task)
		if err != nil {
			return err
		}
	}
	payload, err := s.runRole(ctx, task, phase, phase.Owner, data, validate)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	stageID := phase.Name
	if phase.Kind == "agent" {
		stageID = phase.Owner
	}
	_, err = s.completeAgentPhase(ctx, task, phase, stageID, phase.Owner, validate, payload, func(payload string) error {
		switch phase.Owner {
		case "builder":
			return s.validateBuilderPaths(ctx, task, profile)
		case "reviewer":
			review, validateErr := ValidateReview(payload)
			if validateErr != nil {
				return validateErr
			}
			if !review.Approved {
				return fmt.Errorf("reviewer rejected implementation")
			}
		}
		if phase.Owner == "planner" || phase.Owner == "reviewer" {
			if isReadOnlyOwner(phase.Owner) {
				after, changedErr := repositoryFingerprint(ctx, s.git, task)
				if changedErr != nil {
					return changedErr
				}
				if baseline != after {
					return fmt.Errorf("%s modified repository", phase.Owner)
				}
			}
		}
		return nil
	}, stateAfterRole(phase.Owner))
	if err != nil {
		return err
	}
	if phase.Owner == "builder" {
		return s.continueAfterBuilder(ctx, task.ID)
	}
	return nil
}

func (s *Service) runRetryChecks(ctx context.Context, task store.Task, phase store.Phase) error {
	profile, err := readTaskProfile(task)
	if err != nil {
		return err
	}
	if err = s.quality.runChecks(ctx, task, phase, profile.Checks, "primary", ""); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	lock := s.taskLock(task.ID)
	lock.Lock()
	if err = s.endPhase(ctx, phase, "success", nil); err == nil {
		err = s.db.Transition(ctx, task.ID, string(Checking), string(Reviewing), "", "")
	}
	lock.Unlock()
	if err != nil {
		return err
	}
	return s.continueAfterBuilder(ctx, task.ID)
}

func stateForPhase(phase store.Phase) State {
	switch phase.Name {
	case "planning":
		return Planning
	case "checks":
		return Checking
	case "reviewing":
		return Reviewing
	case "building":
		return Building
	default:
		return stateForRole(phase.Owner)
	}
}

func (s *Service) traceMessage(ctx context.Context, message store.Message, phase *store.Phase) error {
	event, err := s.messageEvent(ctx, message, phase)
	if err != nil {
		return err
	}
	_, err = s.db.AppendEvent(ctx, s.taskDir(message.TaskID), event)
	return err
}

func (s *Service) messageEvent(ctx context.Context, message store.Message, phase *store.Phase) (store.Event, error) {
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
	return store.Event{
		ID: randomID(), TaskID: message.TaskID, PhaseID: phaseID, AttemptID: phaseID, BranchID: branchID,
		Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display,
		AvailableActions: AvailableActions(phase, taskState), StartedAt: time.Now().UTC(),
	}, nil
}

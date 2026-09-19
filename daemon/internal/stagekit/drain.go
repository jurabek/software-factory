package stagekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// DrainSpec parameterizes a message-continuation drain for one stage.
type DrainSpec struct {
	Task         store.Task
	Phase        store.Phase
	StageID      string
	AgentName    string
	Role         string
	ReadOnly     bool
	Instructions string
	Validate     func(string) (any, error)
	// OnValid lets a stage persist evidence from the final payload.
	OnValid func(ctx context.Context, payload string) error
}

// Drain delivers queued messages to a running stage's agent session until the
// queue is empty, returning the latest valid payload.
func (k *Kit) Drain(ctx context.Context, spec DrainSpec) (string, error) {
	task := spec.Task
	phase := spec.Phase
	configured, err := k.resolveConfig(task)
	if err != nil {
		return "", err
	}
	agent, ok := configured.Agent(spec.AgentName)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", spec.AgentName)
	}
	adapter, ok := k.harnesses.Get(configured.Defaults.CodingAgent)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", configured.Defaults.CodingAgent)
	}
	storedSession, err := k.db.AgentSession(ctx, task.ID, spec.StageID)
	if err != nil {
		return "", err
	}
	var latest string
	for {
		message, nextErr := k.db.NextQueuedMessage(ctx, task.ID, spec.StageID)
		if errors.Is(nextErr, store.ErrNotFound) {
			return latest, nil
		}
		if nextErr != nil {
			return "", nextErr
		}
		if message.AgentSessionID != storedSession.HarnessSessionID {
			err = fmt.Errorf("message agent session identity is stale")
			k.failMessage(ctx, message, phase, "session_unavailable")
			return "", err
		}
		systemPrompt, promptErr := k.messageSystemPrompt(ctx, message, spec.Role, spec.Instructions)
		if promptErr != nil {
			k.failMessage(ctx, message, phase, "context_unavailable")
			return "", promptErr
		}
		request := harness.Request{
			CWD: task.RepositoryPath, Prompt: message.Text, SystemPrompt: systemPrompt,
			Model: agent.Model, Thinking: agent.Thinking, SessionID: storedSession.HarnessSessionID,
			SessionDirectory: storedSession.SessionDirectory, RawOutputPath: filepath.Join(storedSession.SessionDirectory, "raw-output.jsonl"),
			DeadlineMS: configured.Runtime.AgentDeadlineMS, Resume: true,
		}
		for correction := 0; correction <= configured.Runtime.JSONFixAttempts; correction++ {
			var before string
			if spec.ReadOnly {
				before, err = fingerprint(ctx, k.git, task)
				if err != nil {
					return "", err
				}
			}
			invocationID := uuid.New().String()
			if correction == 0 {
				message.DeliveryStatus = "delivered"
				message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
				event, eventErr := MessageEvent(ctx, k.db, message, &phase)
				if eventErr != nil {
					return "", eventErr
				}
				if err = k.db.BeginMessageInvocationWithEvent(ctx, task.ID, spec.StageID, invocationID, message.ID, event, k.TaskDir(task.ID)); err != nil {
					return "", err
				}
			} else if err = k.db.BeginAgentInvocation(ctx, task.ID, spec.StageID, invocationID); err != nil {
				return "", err
			}
			result, runErr := agentexec.Invoke(ctx, adapter, request, k.Sink(task.ID, phase.ID, storedSession.Harness))
			if spec.ReadOnly {
				after, fingerprintErr := fingerprint(ctx, k.git, task)
				if fingerprintErr != nil {
					runErr = errors.Join(runErr, fingerprintErr)
				} else if before != after {
					runErr = errors.Join(runErr, fmt.Errorf("%s modified repository", spec.StageID))
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
			finalizeErr := k.db.FinalizeAgentInvocation(cleanupCtx, task.ID, spec.StageID, invocationID, store.AgentSession{
				StageID: spec.StageID, AgentName: spec.AgentName, Role: spec.AgentName, Harness: storedSession.Harness, Provider: result.Provider, Model: result.Model, Thinking: agent.Thinking, Color: agent.Color,
				HarnessSessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, SessionReady: storedSession.SessionReady,
				NativeTranscriptPath: result.NativeTranscriptPath, ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow,
				Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost, AccountingComplete: result.AccountingComplete,
			})
			cancel()
			if finalizeErr != nil {
				k.failMessage(ctx, message, phase, "delivery_failed")
				return "", finalizeErr
			}
			if runErr != nil {
				reason := "harness_error"
				if sessionMismatch {
					reason = "session_identity_changed"
				} else if errors.Is(runErr, context.Canceled) {
					reason = "invocation_cancelled"
				}
				k.failMessage(ctx, message, phase, reason)
				return "", runErr
			}
			_, validationErr := spec.Validate(result.Text)
			envelopeRole := phaseEnvelopeKind(phase, spec.Role)
			if err = k.db.SaveEnvelope(ctx, RandomID(), task.ID, phase.ID, spec.StageID, envelopeRole, result.Text, validationErr == nil, correction+1); err != nil {
				k.failMessage(ctx, message, phase, "delivery_failed")
				return "", err
			}
			if validationErr == nil {
				if spec.OnValid != nil {
					if evidenceErr := spec.OnValid(ctx, result.Text); evidenceErr != nil {
						k.failMessage(ctx, message, phase, "evidence_persistence_failed")
						return "", evidenceErr
					}
				}
				latest = result.Text
				break
			}
			if correction == configured.Runtime.JSONFixAttempts {
				k.failMessage(ctx, message, phase, "invalid_agent_response")
				return "", fmt.Errorf("%s envelope invalid after corrections: %w", spec.StageID, validationErr)
			}
			request.Prompt = "Your previous final response was invalid: " + validationErr.Error() + "\n" + spec.Instructions
		}
	}
}

func (k *Kit) messageSystemPrompt(ctx context.Context, message store.Message, role, instructions string) (string, error) {
	contextValue := map[string]any{}
	if message.Target != nil {
		contextValue["target"] = message.Target
	}
	checks, err := k.db.Checks(ctx, message.TaskID)
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
		return instructions, nil
	}
	encoded, err := json.Marshal(contextValue)
	if err != nil {
		return "", fmt.Errorf("encode message context: %w", err)
	}
	return instructions + "\n\nFactory context for this turn: " + string(encoded), nil
}

func (k *Kit) failMessage(ctx context.Context, message store.Message, phase store.Phase, reason string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message.DeliveryStatus = "failed"
	message.FailureReason = reason
	event, err := MessageEvent(cleanupCtx, k.db, message, &phase)
	if err != nil {
		return
	}
	_, _ = k.db.FailMessageWithEvent(cleanupCtx, message.TaskID, message.ID, reason, event, k.TaskDir(message.TaskID))
}

// MessageEvent builds the history event recorded for a task message.
func MessageEvent(ctx context.Context, db *store.DB, message store.Message, phase *store.Phase) (store.Event, error) {
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
	if task, taskErr := db.Task(ctx, message.TaskID); taskErr == nil {
		taskState = task.State
	}
	return store.Event{
		ID: RandomID(), TaskID: message.TaskID, PhaseID: phaseID, AttemptID: phaseID, BranchID: branchID,
		Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display,
		AvailableActions: AvailableActions(phase, taskState), StartedAt: time.Now().UTC(),
	}, nil
}

func phaseEnvelopeKind(phase store.Phase, role string) string {
	if phase.Kind == "build" || phase.Kind == "review" {
		return phase.Kind
	}
	return role
}

func fingerprint(ctx context.Context, runner factorygit.Runner, task store.Task) (string, error) {
	if task.RepositoryPath == "" {
		return "", nil
	}
	return factorygit.Fingerprint(ctx, runner, task.RepositoryPath)
}

func persistedUsage(value harness.Usage) session.Usage {
	return session.Usage{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Reasoning: value.Reasoning, TotalTokens: value.TotalTokens}
}

package stagekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
// queue is empty, returning the latest valid turn. Execution mechanics are
// owned by the harness turn loop; Drain owns only message bookkeeping.
func (k *Kit) Drain(ctx context.Context, spec DrainSpec) (harness.TurnResult, error) {
	task := spec.Task
	phase := spec.Phase
	configured, err := k.resolveConfig(task)
	if err != nil {
		return harness.TurnResult{}, err
	}
	agent, ok := configured.Agent(spec.AgentName)
	if !ok {
		return harness.TurnResult{}, fmt.Errorf("agent %s not configured", spec.AgentName)
	}
	storedSession, err := k.db.AgentSession(ctx, task.ID, spec.StageID)
	if err != nil {
		return harness.TurnResult{}, err
	}
	var latest harness.TurnResult
	for {
		message, nextErr := k.db.NextQueuedMessage(ctx, task.ID, spec.StageID)
		if errors.Is(nextErr, store.ErrNotFound) {
			return latest, nil
		}
		if nextErr != nil {
			return harness.TurnResult{}, nextErr
		}
		if message.AgentSessionID != storedSession.HarnessSessionID {
			err = fmt.Errorf("message agent session identity is stale")
			k.failMessage(ctx, message, phase, "session_unavailable")
			return harness.TurnResult{}, err
		}
		systemPrompt, promptErr := k.messageSystemPrompt(ctx, message, spec.Role, spec.Instructions)
		if promptErr != nil {
			k.failMessage(ctx, message, phase, "context_unavailable")
			return harness.TurnResult{}, promptErr
		}
		delivered := false
		onDispatch := func(dispatchCtx context.Context, _ string) error {
			if delivered {
				return nil
			}
			delivered = true
			message.DeliveryStatus = "delivered"
			message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
			event, eventErr := MessageEvent(dispatchCtx, k.db, message, &phase)
			if eventErr != nil {
				return eventErr
			}
			return k.db.DeliverMessageWithEvent(dispatchCtx, task.ID, message.ID, event, k.TaskDir(task.ID))
		}
		turner := k.AgentExec()
		turner.AgentDeadlineMS = configured.Runtime.AgentDeadlineMS
		turner.JSONFixAttempts = configured.Runtime.JSONFixAttempts
		turn, runErr := harness.RunTurn(ctx, turner, harness.TurnInput{
			TaskID: task.ID, RequestID: RandomID(), Phase: phase, Role: spec.Role,
			HarnessName: configured.Defaults.CodingAgent, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
			RepoPath: task.RepositoryPath, SessionDir: storedSession.SessionDirectory,
			SystemPrompt: systemPrompt, UserPrompt: message.Text,
			ReadOnly: spec.ReadOnly, EnvelopeKind: phaseEnvelopeKind(phase, spec.Role),
			CorrectionSuffix: spec.Instructions, Validate: spec.Validate,
			Sink: k.Sink(task.ID, phase.ID, storedSession.Harness), OnDispatch: onDispatch,
		})
		if runErr != nil {
			reason := "harness_error"
			if errors.Is(runErr, context.Canceled) {
				reason = "invocation_cancelled"
			}
			k.failMessage(ctx, message, phase, reason)
			return harness.TurnResult{}, runErr
		}
		if spec.OnValid != nil {
			if evidenceErr := spec.OnValid(ctx, turn.Payload); evidenceErr != nil {
				k.failMessage(ctx, message, phase, "evidence_persistence_failed")
				return harness.TurnResult{}, evidenceErr
			}
		}
		latest = turn
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
		DeliveryStatus: message.DeliveryStatus, FailureReason: message.FailureReason,
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

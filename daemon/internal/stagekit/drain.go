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

// Delivery describes how queued messages for one stage become agent turns:
// the phase to run, the role that renders prompts, read-only enforcement, the
// instructions appended to the system prompt, the envelope validator, and an
// optional persistence hook for stage evidence.
type Delivery struct {
	Task         store.Task
	Phase        store.Phase
	Role         string
	ReadOnly     bool
	Instructions string
	Validate     func(string) (any, error)
	// OnValid persists stage evidence from the final payload of each turn.
	OnValid func(ctx context.Context, payload string) error
}

// DeliverAndFinalize delivers every queued message as one agent turn, then
// runs finalize under the task lock once the queue is empty. A message queued
// while finalizing restarts the loop. finalize completes the phase and returns
// the stage result; delivery failures mark the phase failed.
//
//	Stage            DeliverAndFinalize       DB            RunTurn        pi process
//	  |                     |                 |               |               	|
//	  |--Delivery+finalize->|                 |               |               	|
//	  |                     |--deliver()----->|               |               	|
//	  |                     |<-message/none---|               |               	|
//	  |                     |--RunTurn(msg)------------------>|               	|
//	  |                     |                 |  OnDispatch: markDelivered      |
//	  |                     |                 |               |--prompt(stdin)> |
//	  |                     |                 |               |<-events+settled |
//	  |                     |<-TurnResult---------------------|               	|
//	  |                     |   ...repeat until queue empty...                	|
//	  |                     |--NextQueuedMessage (locked)--->|               	|
//	  |                     |<-none--------------------------|               	|
//	  |                     |--finalize(turn)  [complete phase, transition]  	|
//	  |<-result-------------|                 |               |               	|
func (k *Kit) DeliverAndFinalize[T any](ctx context.Context, spec Delivery, turn harness.TurnResult, finalize func(harness.TurnResult) (T, error)) (T, error) {
	var zero T
	for {
		latest, err := k.deliver(ctx, spec)
		if err != nil {
			k.Fail(ctx, spec.Phase, err)
			return zero, err
		}
		if latest.Payload != "" {
			turn = latest
		}
		lock := k.Lock(spec.Task.ID)
		lock.Lock()
		_, err = k.db.NextQueuedMessage(ctx, spec.Task.ID, spec.Phase.Name)
		if err == nil {
			lock.Unlock()
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			lock.Unlock()
			k.Fail(ctx, spec.Phase, err)
			return zero, err
		}
		result, finalizeErr := finalize(turn)
		lock.Unlock()
		return result, finalizeErr
	}
}

// deliver runs one agent turn per queued message, in queue order, returning
// the latest settled payload (zero TurnResult when nothing was queued).
// Execution mechanics live in harness.RunTurn; deliver owns only message
// bookkeeping.
func (k *Kit) deliver(ctx context.Context, spec Delivery) (harness.TurnResult, error) {
	task, phase := spec.Task, spec.Phase
	configured, err := k.resolveConfig(task)
	if err != nil {
		return harness.TurnResult{}, err
	}
	agent, ok := configured.Agent(phase.Owner)
	if !ok {
		return harness.TurnResult{}, fmt.Errorf("agent %s not configured", phase.Owner)
	}
	storedSession, err := k.db.AgentSession(ctx, task.ID, phase.Name)
	if err != nil {
		return harness.TurnResult{}, err
	}
	var latest harness.TurnResult
	for {
		message, nextErr := k.db.NextQueuedMessage(ctx, task.ID, phase.Name)
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
			Sink:       k.Sink(task.ID, phase.ID, storedSession.Harness),
			OnDispatch: k.markDelivered(message, &phase),
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

// markDelivered returns the OnDispatch hook that durably records one message's
// delivery in the same window as the invocation marker. It fires once, on the
// first correction attempt.
func (k *Kit) markDelivered(message store.Message, phase *store.Phase) func(context.Context, string) error {
	delivered := false
	return func(dispatchCtx context.Context, _ string) error {
		if delivered {
			return nil
		}
		delivered = true
		message.DeliveryStatus = "delivered"
		message.DeliveredAt = time.Now().UTC().Format(time.RFC3339Nano)
		event, err := MessageEvent(dispatchCtx, k.db, message, phase)
		if err != nil {
			return err
		}
		return k.db.DeliverMessageWithEvent(dispatchCtx, message.TaskID, message.ID, event, k.TaskDir(message.TaskID))
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

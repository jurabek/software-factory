package stagekit

import (
	"context"
	"time"

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

// deliver runs one agent turn per queued message, in queue order, returning
// the latest settled payload (zero TurnResult when nothing was queued).
// Execution mechanics live in harness.RunTurn; deliver owns only message
// bookkeeping.

// markDelivered returns the OnDispatch hook that durably records one message's
// delivery in the same window as the invocation marker. It fires once, on the
// first correction attempt.

// MessageEvent builds the history event recorded for a task message.
func MessageEvent(ctx context.Context, db *store.Store, message store.Message, phase *store.Phase) (store.Event, error) {
	phaseID := ""
	if phase != nil {
		phaseID = phase.ID
	}
	entry := session.NewTaskMessage(session.TaskMessagePayload{
		MessageID: message.ID, TaskID: message.TaskID, Text: message.Text, RecipientRole: message.RecipientRole,
		AgentSessionID: message.AgentSessionID, TargetType: message.TargetType, TargetID: message.TargetID,
		DeliveryStatus: message.DeliveryStatus, FailureReason: message.FailureReason,
	})
	taskState := ""
	if task, taskErr := db.Tasks.Get(ctx, message.TaskID); taskErr == nil {
		taskState = task.State
	}
	return store.Event{
		ID: RandomID(), TaskID: message.TaskID, PhaseID: phaseID, AttemptID: phaseID,
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

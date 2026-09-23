package store

import (
	"database/sql"
	"errors"
)

type Message struct {
	Sequence       int64          `json:"sequence"`
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id"`
	Actor          string         `json:"actor"`
	Text           string         `json:"text"`
	IdempotencyKey string         `json:"idempotency_key"`
	TargetType     string         `json:"-"`
	TargetID       string         `json:"-"`
	Target         *MessageTarget `json:"target,omitempty"`
	StageID        string         `json:"stage_id,omitempty"`
	RecipientRole  string         `json:"recipient_role"`
	AgentSessionID string         `json:"agent_session_id"`
	DeliveryStatus string         `json:"delivery_status"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	CreatedAt      string         `json:"created_at"`
	DeliveredAt    string         `json:"delivered_at,omitempty"`
	FailedAt       string         `json:"failed_at,omitempty"`
}
type MessageTarget struct {
	AttemptID string `json:"attempt_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// AcceptMessageWithEvent stores a new Message, applies the associated Task
// lifecycle changes, and records its history event in one transaction. The
// event trace is derived output and is written only after the commit.

// The database is authoritative; trace export is derived output.

func scanMessage(scanner interface{ Scan(...any) error }) (Message, error) {
	var value Message
	err := scanner.Scan(&value.Sequence, &value.ID, &value.TaskID, &value.Actor, &value.Text, &value.IdempotencyKey, &value.TargetType, &value.TargetID, &value.StageID, &value.RecipientRole, &value.AgentSessionID, &value.DeliveryStatus, &value.FailureReason, &value.CreatedAt, &value.DeliveredAt, &value.FailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err == nil && value.TargetType != "" {
		value.Target = &MessageTarget{}
		switch value.TargetType {
		case "attempt":
			value.Target.AttemptID = value.TargetID
		case "event":
			value.Target.EventID = value.TargetID
		}
	}
	return value, wrap("read message", err)
}

// QueuedMessageForStages reports whether a queued message is routed to any of
// the given stage identifiers.

// BeginMessageInvocationWithEvent delivers a queued message and records its
// delivery event in the same database transaction. The file trace is derived
// output and is written only after the transaction commits.

// DeliverMessageWithEvent marks a queued message delivered and records its
// delivery event without touching the agent-session invocation marker. The
// caller records that marker separately, so a message dispatch and an agent
// invocation that share a transaction window stay distinct.

// FailMessageWithEvent publishes a message failure and its lifecycle event in
// one transaction. The event trace is derived output and is written only
// after the database commit.

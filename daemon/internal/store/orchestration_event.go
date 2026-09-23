package store

// OrchestrationEvent is a durable command for the background task controller.
// Delivery is at-least-once; handlers must derive their effects from task state.
type OrchestrationEvent struct {
	ID     string
	TaskID string
	Type   string
}

const (
	TaskCreated   = "task_created"
	TaskPaused    = "task_paused"
	TaskCancelled = "task_cancelled"
	TaskMessaged  = "task_messaged"
	TaskApproved  = "task_approved"
	TaskResumed   = "task_resumed"
	TaskRetried   = "task_retried"
)

// EnqueueOrchestrationEvent records a command after its durable task mutation.

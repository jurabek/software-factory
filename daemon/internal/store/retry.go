package store

type RetryResult struct {
	SourceAttemptID string `json:"source_attempt_id"`
	BranchID        string `json:"branch_id"`
	AttemptID       string `json:"attempt_id"`
	CreatedAt       string `json:"created_at"`
}

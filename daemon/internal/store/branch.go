package store

type Branch struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	ParentBranchID string `json:"parent_branch_id,omitempty"`
	ForkAttemptID  string `json:"fork_attempt_id,omitempty"`
	HeadAttemptID  string `json:"head_attempt_id,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

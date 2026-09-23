package store

type Envelope struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	PhaseID    string `json:"phase_id"`
	StageID    string `json:"stage_id,omitempty"`
	AgentRole  string `json:"agent_role"`
	OutputType string `json:"output_type"`
	Payload    string `json:"payload"`
	CreatedAt  string `json:"created_at"`
	Valid      bool   `json:"valid"`
	Attempt    int    `json:"attempt"`
}

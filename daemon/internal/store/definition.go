package store

type PhaseDefinition struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	PhaseKey       string `json:"phase_key"`
	Revision       int    `json:"revision"`
	Executor       string `json:"executor"`
	Owner          string `json:"owner"`
	Spec           string `json:"spec_json"`
	Digest         string `json:"digest"`
	ParentRevision int    `json:"parent_revision"`
	CreatedAt      string `json:"created_at"`
}

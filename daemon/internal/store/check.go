package store

type Check struct {
	ID                 string `json:"id"`
	TaskID             string `json:"task_id"`
	PhaseID            string `json:"phase_id"`
	StageID            string `json:"stage_id"`
	Phase              string `json:"phase"`
	ComparisonBaseline string `json:"comparison_baseline,omitempty"`
	Name               string `json:"name"`
	Command            string `json:"command"`
	Status             string `json:"status"`
	Output             string `json:"output"`
	OutputPath         string `json:"output_path"`
	Attempt            int    `json:"attempt"`
	ExitCode           int    `json:"exit_code"`
	DurationMS         int    `json:"duration_ms"`
	StartedAt          string `json:"started_at"`
	EndedAt            string `json:"ended_at"`
}

package store

type TestChange struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	PhaseID    string `json:"phase_id"`
	Attempt    int    `json:"attempt"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	ChangeKind string `json:"change_kind"`
	RenameFrom string `json:"rename_from,omitempty"`
	RenameTo   string `json:"rename_to,omitempty"`
	CreatedAt  string `json:"created_at"`
}
type Comparison struct {
	ID               string   `json:"id"`
	TaskID           string   `json:"task_id"`
	PhaseID          string   `json:"phase_id"`
	Attempt          int      `json:"attempt"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	BaselineSnapshot string   `json:"baseline_snapshot,omitempty"`
	OverlayPaths     []string `json:"overlay_paths"`
	CreatedAt        string   `json:"created_at"`
	DurationMS       int      `json:"duration_ms"`
}

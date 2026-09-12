package factory

import "context"

// Sandbox materializes repositories into an isolated task workspace.
type Sandbox interface {
	Materialize(context.Context, MaterializationRequest) (Materialization, error)
	Cleanup(context.Context, CleanupRequest) error
}

type MaterializationRequest struct {
	TaskID       string
	RepositoryID string
	Name         string
	SourceType   string
	Source       string
	Destination  string
}

type Materialization struct {
	Root                  string   `json:"root"`
	SourceType            string   `json:"source_type"`
	Source                string   `json:"source"`
	BaseSHA               string   `json:"base_sha"`
	BranchName            string   `json:"branch_name"`
	Checks                []Check  `json:"checks"`
	Generated             []string `json:"generated"`
	Protected             []string `json:"protected"`
	Tests                 []string `json:"tests"`
	PreChangeVerification bool     `json:"pre_change_verification"`
	Instructions          []string `json:"instructions"`
}

type Check struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

type CleanupRequest struct {
	TaskID        string
	WorkspaceRoot string
	Repositories  []CleanupRepository
}

type CleanupRepository struct {
	RepositoryID  string
	Name          string
	SourceType    string
	CanonicalPath string
	WorkingPath   string
}

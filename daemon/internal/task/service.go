// Package task owns task identity and lifecycle at the data layer: creation,
// filesystem layout, metadata, repository normalization, branch selection,
// deletion, and diffs. The orchestrator delegates task CRUD here.
package task

import (
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/workspace"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// Repository describes the source a task is created from.
type Repository struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Repo string `json:"repo,omitempty"`
}

// CreateRequest is the control-plane request to create a task.
type CreateRequest struct {
	Request     string     `json:"request"`
	Repository  Repository `json:"repository"`
	Pipeline    string     `json:"pipeline,omitempty"`
	CodingAgent string     `json:"coding_agent,omitempty"`
	Model       string     `json:"model,omitempty"`
	Thinking    string     `json:"thinking,omitempty"`
}

// CreateSessionRequest starts a child task reusing the parent's repository and
// agent selection.
type CreateSessionRequest struct {
	Request string `json:"request"`
}

// Diff is the changed-file and patch view of a task branch.
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

// Deps are the collaborators a task service needs.
type Deps struct {
	Store      *store.DB
	Config     config.Config
	ConfigPath string
	Harnesses  harness.Registry
	Git        factorygit.Runner
	Sandbox    workspace.Sandbox
}

// Service creates and manages task records and their filesystem.
type Service struct {
	root string
	deps Deps
}

// New constructs a task service rooted at the factory root.
func New(root string, deps Deps) *Service {
	return &Service{root: root, deps: deps}
}

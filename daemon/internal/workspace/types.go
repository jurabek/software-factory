// Package workspace owns repository materialization contracts shared by task
// orchestration and stage implementations.
package workspace

import (
	"context"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
)

type Sandbox interface {
	Materialize(context.Context, MaterializationRequest) (Materialization, error)
	Cleanup(context.Context, CleanupRequest) error
}

type MaterializationRequest struct {
	TaskID      string
	SourceType  string
	Source      string
	Destination string
}

// Materialization and Check are the repository profile types owned by the git
// adapter; workspace re-exports them so task orchestration stays git-agnostic.
type (
	Materialization = factorygit.Profile
	Check           = factorygit.Check
)

type CleanupRequest struct {
	TaskID        string
	WorkspaceRoot string
	SourceType    string
	CanonicalPath string
	WorkingPath   string
}

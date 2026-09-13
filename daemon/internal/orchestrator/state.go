package orchestrator

import "github.com/jurabek/software-factory/daemon/internal/stagekit"

// State is the task lifecycle state owned by the stage modules. The
// orchestrator keeps aliases for control-plane checks and tests.
type State = stagekit.State

const (
	Preparing        = stagekit.Preparing
	Planning         = stagekit.Planning
	AwaitingApproval = stagekit.AwaitingApproval
	Building         = stagekit.Building
	Checking         = stagekit.Checking
	Reviewing        = stagekit.Reviewing
	Completed        = stagekit.Completed
	Blocked          = stagekit.Blocked
	Paused           = stagekit.Paused
	Aborted          = stagekit.Aborted
)

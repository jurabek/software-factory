package agentexec

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// TaskConfig carries resolved task configuration for stage prompt rendering.
type TaskConfig struct {
	Config     config.Config
	ConfigPath string
	TaskDir    string
}

// Configurer resolves task configuration without the orchestrator.
type Configurer interface {
	TaskConfig(ctx context.Context, task store.Task) (TaskConfig, error)
}

// SinkFactory opens a live-event sink for a turn.
type SinkFactory func(taskID, phaseID, harness string) harness.EventSink

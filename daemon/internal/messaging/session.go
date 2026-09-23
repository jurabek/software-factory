package messaging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func (s *Service) ensureAgentSession(ctx context.Context, task store.Task, role string) (store.AgentSession, error) {
	configured, err := config.Resolve(s.deps.Config, s.deps.ConfigPath, task.ConfigSnapshot)
	if err != nil {
		return store.AgentSession{}, err
	}
	agentName := role
	if _, pipeline, pipelineErr := config.TaskPipeline(s.deps.Config, s.deps.ConfigPath, task.ConfigSnapshot, task.Pipeline); pipelineErr == nil {
		if stage, _, ok := stageDefinition(pipeline, role); ok && stage.Agent != "" {
			agentName = stage.Agent
		}
	}
	agent, ok := configured.Agent(agentName)
	if !ok {
		return store.AgentSession{}, fmt.Errorf("agent %s not configured", agentName)
	}
	harnessName := configured.Defaults.CodingAgent
	if _, ok = s.deps.Harnesses.Get(harnessName); !ok {
		return store.AgentSession{}, fmt.Errorf("harness %s unavailable", harnessName)
	}
	stored, err := s.deps.Store.AgentSession(ctx, task.ID, role)
	if err == nil {
		if stored.Harness != harnessName {
			return store.AgentSession{}, store.ErrConflict
		}
		return stored, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.AgentSession{}, err
	}
	return s.deps.Store.ReserveAgentSession(ctx, task.ID, store.AgentSession{
		StageID: role, AgentName: agentName, Role: agentName, Harness: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		HarnessSessionID: uuid.New().String(), SessionDirectory: filepath.Join(s.taskDir(task.ID), "sessions", role, harnessName),
	})
}

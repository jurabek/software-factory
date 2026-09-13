package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/verifier"
	"gopkg.in/yaml.v3"
)

func fallbackPipelineConfig(c config.Config) config.Config {
	if len(c.Pipelines) > 0 {
		return c
	}
	available := map[string]bool{}
	for _, agent := range c.Agents {
		available[agent.Name] = true
	}
	agent := func(preferred string) string {
		if available[preferred] {
			return preferred
		}
		if len(c.Agents) > 0 {
			return c.Agents[0].Name
		}
		return preferred
	}
	stages := []config.Stage{
		{ID: "plan", Kind: "plan", Agent: agent("planner")},
		{ID: "build", Kind: "build", Agent: agent("builder")},
		{ID: "check", Kind: "verify"},
		{ID: "review", Kind: "review", Agent: agent("reviewer")},
	}
	return config.Config{
		Defaults:  c.Defaults,
		Runtime:   c.Runtime,
		Agents:    c.Agents,
		Pipelines: []config.Pipeline{{Name: "standard", Default: true, Stages: stages}},
	}
}

func (s *pipelineService) freezeConfig() (config.Config, error) {
	c := fallbackPipelineConfig(s.config)
	base := filepath.Dir(s.configPath)
	for index := range c.Agents {
		agent := &c.Agents[index]
		if agent.PromptEngineering.SystemContent == "" && agent.PromptEngineering.System != "" && s.configPath != "" {
			body, err := os.ReadFile(filepath.Join(base, agent.PromptEngineering.System))
			if err != nil {
				return config.Config{}, fmt.Errorf("read %s system prompt: %w", agent.Name, err)
			}
			agent.PromptEngineering.SystemContent = string(body)
		}
		if agent.PromptEngineering.UserContent == "" && agent.PromptEngineering.User != "" && s.configPath != "" {
			body, err := os.ReadFile(filepath.Join(base, agent.PromptEngineering.User))
			if err != nil {
				return config.Config{}, fmt.Errorf("read %s user prompt: %w", agent.Name, err)
			}
			agent.PromptEngineering.UserContent = string(body)
		}
	}
	return c, nil
}

func (s *pipelineService) selectPipeline(name string) (config.Config, config.Pipeline, error) {
	c, err := s.freezeConfig()
	if err != nil {
		return config.Config{}, config.Pipeline{}, err
	}
	if name == "" {
		pipeline, ok := c.DefaultPipeline()
		if !ok {
			return config.Config{}, config.Pipeline{}, fmt.Errorf("configuration has no default pipeline")
		}
		return c, pipeline, nil
	}
	pipeline, ok := c.Pipeline(name)
	if !ok {
		return config.Config{}, config.Pipeline{}, fmt.Errorf("unknown pipeline %q", name)
	}
	return c, pipeline, nil
}

func (s *pipelineService) taskPipeline(task store.Task) (config.Config, config.Pipeline, error) {
	c, err := taskConfig(s.config, s.configPath, task)
	if err != nil {
		return config.Config{}, config.Pipeline{}, err
	}
	c = fallbackPipelineConfig(c)
	if len(c.Agents) == 0 {
		return c, config.Pipeline{}, nil
	}
	pipeline, ok := c.Pipeline(task.Pipeline)
	if !ok {
		pipeline, ok = c.DefaultPipeline()
	}
	if !ok {
		return config.Config{}, config.Pipeline{}, fmt.Errorf("task pipeline %q is unavailable", task.Pipeline)
	}
	return c, pipeline, nil
}

func (s *Service) StageProjection(ctx context.Context, task store.Task) ([]store.StageProjection, error) {
	return s.pipelines.StageProjection(ctx, task)
}

func (s *pipelineService) StageProjection(ctx context.Context, task store.Task) ([]store.StageProjection, error) {
	_, pipeline, err := s.taskPipeline(task)
	if err != nil {
		return nil, err
	}
	if len(pipeline.Stages) == 0 {
		return []store.StageProjection{}, nil
	}
	phases, err := s.db.Phases(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	result := make([]store.StageProjection, 0, len(pipeline.Stages))
	for _, stage := range pipeline.Stages {
		value := store.StageProjection{ID: stage.ID, Kind: stage.Kind, Agent: stage.Agent, Status: "not_started"}
		phaseName := stage.ID
		if stage.Kind == "plan" {
			phaseName = "planning"
		}
		for index := len(phases) - 1; index >= 0; index-- {
			phase := phases[index]
			if phase.Name != phaseName || phase.Superseded {
				continue
			}
			value.AttemptID = phase.ID
			if phase.Status == "running" || phase.Status == "queued" {
				value.Status = "running"
			} else if phase.Status == "failed" {
				value.Status = "failed"
				if task.ActiveStage == stage.ID {
					value.BlockingReason = task.Error
				}
			} else if phase.Status == "success" {
				value.Status = "completed"
			}
			break
		}
		if task.ActiveStage == stage.ID {
			switch task.State {
			case string(Paused):
				value.Status = "paused"
			case string(Aborted):
				value.Status = "aborted"
			case string(Blocked):
				if value.Status != "failed" {
					value.Status = "blocked"
					value.BlockingReason = task.Error
				}
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func stageState(kind string) State {
	switch kind {
	case "plan":
		return Planning
	case "build":
		return Building
	case "verify":
		return Checking
	case "review":
		return Reviewing
	default:
		return Blocked
	}
}

func stageDefinition(pipeline config.Pipeline, id string) (config.Stage, int, bool) {
	for index, stage := range pipeline.Stages {
		if stage.ID == id {
			return stage, index, true
		}
	}
	return config.Stage{}, -1, false
}

func (s *Service) prepareOnly(ctx context.Context, task store.Task) error {
	phase, err := s.beginPhase(ctx, task.ID, "prepare", "git", "factory", "Prepare repository")
	if err != nil {
		return err
	}
	repositoryPath, err := s.prepareRepository(ctx, task)
	if err != nil {
		return err
	}
	if err = s.db.SetPrepared(ctx, task.ID, repositoryPath, task.ConfigSnapshot); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.endPhase(ctx, phase, "success", nil); err != nil {
		return err
	}
	return nil
}

func (s *Service) progress(ctx context.Context, taskID string) error {
	if s.pipeliner == nil {
		return fmt.Errorf("pipeline is not configured")
	}
	_, err := s.pipeliner.Run(ctx, taskID)
	return err
}

func (s *Service) continueAfterBuilder(ctx context.Context, taskID string) error {
	return s.progress(ctx, taskID)
}

func (s *Service) transition(ctx context.Context, task store.Task, to State, message string) error {
	if task.State == string(to) {
		return nil
	}
	return s.db.Transition(ctx, task.ID, task.State, string(to), task.ActivePhase, message)
}

func (s *Service) latestStageAttempt(ctx context.Context, taskID, stageID string) (store.Phase, bool, error) {
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for index := len(phases) - 1; index >= 0; index-- {
		if phases[index].Name == stageID && !phases[index].Superseded {
			return phases[index], true, nil
		}
	}
	return store.Phase{}, false, nil
}

func (s *Service) executeVerify(ctx context.Context, task store.Task, phase store.Phase) error {
	profile, err := readTaskProfile(task)
	if err != nil {
		return err
	}
	if err = s.quality.runChecks(ctx, task, phase, profile.Checks, "primary", ""); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.quality.runComparisons(ctx, task, phase, profile); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	checks, err := s.db.Checks(ctx, task.ID)
	if err != nil {
		return err
	}
	phaseChecks := make([]store.Check, 0)
	for _, check := range checks {
		if check.PhaseID == phase.ID {
			phaseChecks = append(phaseChecks, check)
		}
	}
	comparisons, err := s.db.Comparisons(ctx, task.ID)
	if err != nil {
		return err
	}
	phaseComparisons := make([]store.Comparison, 0)
	for _, comparison := range comparisons {
		if comparison.PhaseID == phase.ID {
			phaseComparisons = append(phaseComparisons, comparison)
		}
	}
	report, err := verificationReport(phaseChecks)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	artifact := s.reportArtifact(task, phase, "verification", report, "deterministic-checks")
	return s.completeVerificationPhaseTransitionWithArtifact(ctx, phase, Checking, Reviewing, "success", nil, phaseChecks, phaseComparisons, &artifact)
}

func (s *Service) publishVerificationReport(ctx context.Context, task store.Task, phase store.Phase, checks []store.Check) error {
	report, err := verificationReport(checks)
	if err != nil {
		return err
	}
	return s.publishReport(ctx, task, phase, "verification", report, "deterministic-checks")
}

func verificationReport(checks []store.Check) (string, error) {
	return verifier.Report(checks)
}

func snapshotConfig(c config.Config) (string, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

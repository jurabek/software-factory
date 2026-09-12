package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/store"
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
	if !available["builder"] {
		agent := ""
		if len(c.Agents) > 0 {
			agent = c.Agents[0].Name
		}
		return config.Config{Defaults: c.Defaults, Runtime: c.Runtime, Agents: c.Agents, Pipelines: []config.Pipeline{{Name: "standard", Default: true, Stages: []config.Stage{{ID: "build", Kind: "build", Agent: agent}, {ID: "check", Kind: "verify"}}}}}
	}
	stages := []config.Stage{{ID: "build", Kind: "build", Agent: "builder"}, {ID: "check", Kind: "verify"}}
	if available["reviewer"] {
		stages = append(stages, config.Stage{ID: "review", Kind: "review", Agent: "reviewer"})
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
		for index := len(phases) - 1; index >= 0; index-- {
			phase := phases[index]
			if phase.Name != stage.ID || phase.Superseded {
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

func (s *Service) freezeConfig() (config.Config, error) {
	return s.pipelines.freezeConfig()
}

func (s *Service) selectPipeline(name string) (config.Config, config.Pipeline, error) {
	return s.pipelines.selectPipeline(name)
}

func (s *Service) taskPipeline(task store.Task) (config.Config, config.Pipeline, error) {
	return s.pipelines.taskPipeline(task)
}

func stageState(kind string) State {
	switch kind {
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
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.RepositoryPath == "" {
		if err = s.prepareOnly(ctx, task); err != nil {
			return err
		}
		task, err = s.db.Task(ctx, taskID)
		if err != nil {
			return err
		}
	}
	_, pipeline, err := s.taskPipeline(task)
	if err != nil {
		return err
	}
	for {
		task, err = s.db.Task(ctx, taskID)
		if err != nil {
			return err
		}
		if task.State == string(Paused) || task.State == string(Aborted) || task.State == string(Blocked) || task.State == string(AwaitingApproval) {
			return nil
		}
		stageID := task.ActiveStage
		if stageID == "" {
			if task.State == string(Preparing) {
				if err = s.db.Transition(ctx, taskID, task.State, string(Planning), "", ""); err != nil {
					return err
				}
				return s.plan(ctx, task, nil)
			}
			if task.State == string(Planning) {
				return s.plan(ctx, task, nil)
			}
			if len(pipeline.Stages) == 0 {
				return fmt.Errorf("task pipeline has no stages")
			}
			stageID = pipeline.Stages[0].ID
		}
		stage, index, ok := stageDefinition(pipeline, stageID)
		if !ok {
			return fmt.Errorf("active stage %q is not in task pipeline", stageID)
		}
		if err = s.db.SetActiveStage(ctx, taskID, stage.ID); err != nil {
			return err
		}
		attempt, hasAttempt, err := s.latestStageAttempt(ctx, taskID, stage.ID)
		if err != nil {
			return err
		}
		if hasAttempt && attempt.Status == "success" {
			if index+1 == len(pipeline.Stages) {
				return s.transition(ctx, task, Completed, "")
			}
			next := pipeline.Stages[index+1]
			if err = s.transition(ctx, task, stageState(next.Kind), ""); err != nil {
				return err
			}
			if err = s.db.SetActiveStage(ctx, taskID, next.ID); err != nil {
				return err
			}
			continue
		}
		if hasAttempt && (attempt.Status == "running" || attempt.Status == "queued") {
			return nil
		}
		if err = s.transition(ctx, task, stageState(stage.Kind), ""); err != nil {
			return err
		}
		if err = s.executeStage(ctx, task, stage); err != nil {
			return err
		}
	}
}

func (s *Service) continueAfterBuilder(ctx context.Context, taskID string) error {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.ActiveStage != "" {
		return s.progress(ctx, taskID)
	}
	return s.buildCheckReview(ctx, taskID)
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

func (s *Service) executeStage(ctx context.Context, task store.Task, stage config.Stage) error {
	phase, err := s.beginPhase(ctx, task.ID, stage.ID, stage.Kind, stage.Agent, "Execute "+stage.ID)
	if err != nil {
		return err
	}
	switch stage.Kind {
	case "build":
		return s.executeBuild(ctx, task, stage, phase)
	case "verify":
		return s.executeVerify(ctx, task, phase)
	case "review":
		return s.executeReview(ctx, task, stage, phase)
	default:
		return fmt.Errorf("unsupported stage kind %q", stage.Kind)
	}
}

func (s *Service) executeBuild(ctx context.Context, task store.Task, stage config.Stage, phase store.Phase) error {
	profile, err := readTaskProfile(task)
	if err != nil {
		return err
	}
	data := s.stagePromptData(task)
	data["Plan"] = s.plannerEnvelope(ctx, task)
	payload, err := s.runRole(ctx, task, phase, stage.ID, data, s.builderValidator(ctx, task, profile))
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	_ = payload
	if err = s.validateBuilderPaths(ctx, task, profile); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.persistBuilderEvidence(ctx, task, phase, payload); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	return s.endPhase(ctx, phase, "success", nil)
}

func (s *Service) executeVerify(ctx context.Context, task store.Task, phase store.Phase) error {
	profile, err := readTaskProfile(task)
	if err != nil {
		return err
	}
	if err = s.runChecks(ctx, task, phase, profile.Checks, "primary", ""); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.runComparisons(ctx, task, phase, profile); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	return s.endPhase(ctx, phase, "success", nil)
}

func (s *Service) executeReview(ctx context.Context, task store.Task, stage config.Stage, phase store.Phase) error {
	before, err := repositoryFingerprint(ctx, s.git, task)
	if err != nil {
		return err
	}
	changed, err := taskChangedFiles(ctx, s.git, task, true)
	if err != nil {
		return err
	}
	changes, err := s.diffRepository(ctx, task, true)
	if err != nil {
		return err
	}
	data := s.stagePromptData(task)
	data["Plan"] = s.plannerEnvelope(ctx, task)
	data["Checks"], err = s.db.Checks(ctx, task.ID)
	if err != nil {
		return err
	}
	data["TestChanges"], err = s.db.TestChanges(ctx, task.ID)
	if err != nil {
		return err
	}
	data["Comparisons"], err = s.db.Comparisons(ctx, task.ID)
	if err != nil {
		return err
	}
	data["ChangedFiles"] = changed
	data["Diff"] = changes
	payload, err := s.runRole(ctx, task, phase, stage.ID, data, func(text string) (any, error) { return ValidateReview(text) })
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	review, err := ValidateReview(payload)
	if err != nil || !review.Approved {
		if err == nil {
			err = fmt.Errorf("reviewer rejected implementation")
		}
		s.failPhase(ctx, phase, err)
		return err
	}
	after, err := repositoryFingerprint(ctx, s.git, task)
	if err != nil || before != after {
		if err == nil {
			err = fmt.Errorf("%s modified repository", stage.ID)
		}
		s.failPhase(ctx, phase, err)
		return err
	}
	return s.endPhase(ctx, phase, "success", nil)
}

func (s *Service) stagePromptData(task store.Task) map[string]any {
	return map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}
}

func (s *Service) plannerEnvelope(ctx context.Context, task store.Task) string {
	payload, _ := s.db.ValidEnvelope(ctx, task.ID, "planner")
	return payload
}

func snapshotConfig(c config.Config) (string, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

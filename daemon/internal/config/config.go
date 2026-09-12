package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var validThinking = map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// validHarnesses lists registered coding agents. Pi is implemented;
// codex is reserved so task creation can offer it once its
// adapter lands.
var validHarnesses = map[string]bool{"pi": true, "codex": true}

// IsValidThinking reports whether level is an accepted thinking level.
func IsValidThinking(level string) bool { return validThinking[level] }

// ThinkingLevels returns accepted thinking levels in UI order.
func ThinkingLevels() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// IsValidHarness reports whether name is a known coding agent.
func IsValidHarness(name string) bool { return validHarnesses[name] }

// HarnessNames returns known coding agents in UI order.
func HarnessNames() []string { return []string{"pi", "codex"} }

// ThinkingLevelsFor returns the validated thinking levels a harness supports.
func ThinkingLevelsFor(harness string) []string {
	return ThinkingLevels()
}

// IsValidThinkingFor reports whether a thinking level is valid for a harness.
func IsValidThinkingFor(harness, level string) bool {
	for _, allowed := range ThinkingLevelsFor(harness) {
		if allowed == level {
			return true
		}
	}
	return false
}

// ApplyTaskOverrides returns c with task-level agent/model/thinking applied.
// Empty overrides leave the corresponding field unchanged. A non-empty model
// or thinking override applies to defaults and every role so one task-level
// selection drives planner, builder, and reviewer together.
func ApplyTaskOverrides(c Config, codingAgent, model, thinking string) Config {
	if codingAgent != "" {
		c.Defaults.CodingAgent = codingAgent
	}
	if model != "" {
		c.Defaults.Model = model
		for i := range c.Agents {
			c.Agents[i].Model = model
		}
	}
	if thinking != "" {
		c.Defaults.Thinking = thinking
		for i := range c.Agents {
			c.Agents[i].Thinking = thinking
		}
	}
	return c
}

// Config is the resolved factory configuration.
type Config struct {
	Defaults      Defaults      `yaml:"defaults" json:"defaults"`
	Observability Observability `yaml:"observability" json:"observability"`
	Runtime       Runtime       `yaml:"runtime" json:"runtime"`
	Agents        []Agent       `yaml:"agents" json:"agents"`
	Pipelines     []Pipeline    `yaml:"pipelines" json:"pipelines"`
}
type Defaults struct {
	CodingAgent string `yaml:"coding_agent" json:"coding_agent"`
	Model       string `yaml:"model" json:"model"`
	Thinking    string `yaml:"thinking" json:"thinking"`
}
type Observability struct {
	PollMS int `yaml:"poll_ms" json:"poll_ms"`
}
type Runtime struct {
	AgentDeadlineMS  int `yaml:"agent_deadline_ms" json:"agent_deadline_ms"`
	EmptyTurnRetries int `yaml:"empty_turn_retries" json:"empty_turn_retries"`
	JSONFixAttempts  int `yaml:"json_fix_attempts" json:"json_fix_attempts"`
}
type Agent struct {
	Name              string            `yaml:"name" json:"name"`
	Model             string            `yaml:"model" json:"model"`
	Thinking          string            `yaml:"thinking" json:"thinking"`
	Color             string            `yaml:"color" json:"color"`
	Purpose           string            `yaml:"purpose" json:"purpose"`
	PromptEngineering PromptEngineering `yaml:"prompt_engineering" json:"prompt_engineering"`
}
type PromptEngineering struct {
	System        string `yaml:"system" json:"system"`
	User          string `yaml:"user" json:"user"`
	SystemContent string `yaml:"system_content,omitempty" json:"system_content,omitempty"`
	UserContent   string `yaml:"user_content,omitempty" json:"user_content,omitempty"`
}

type Pipeline struct {
	Name    string  `yaml:"name" json:"name"`
	Default bool    `yaml:"default" json:"default"`
	Stages  []Stage `yaml:"stages" json:"stages"`
}

type Stage struct {
	ID    string `yaml:"id" json:"id"`
	Kind  string `yaml:"kind" json:"kind"`
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
}

// Load reads and validates config.yaml. Prompt contents are intentionally not loaded.
func Load(path string) (Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, filepath.Dir(path))
}

// Parse resolves and validates configuration data against its config directory.
func Parse(data []byte, base string) (Config, []string, error) {
	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, nil, fmt.Errorf("parse config: %w", err)
	}
	resolved := resolve(raw)
	return resolved, validate(resolved, base), nil
}

func resolve(c Config) Config {
	for i := range c.Agents {
		a := &c.Agents[i]
		if a.Model == "" {
			a.Model = c.Defaults.Model
		}
		if a.Thinking == "" {
			a.Thinking = c.Defaults.Thinking
		}
	}
	return c
}

func validate(c Config, base string) []string {
	var problems []string
	if !validHarnesses[c.Defaults.CodingAgent] {
		problems = append(problems, "defaults.coding_agent must be pi or codex")
	}
	if !validThinking[c.Defaults.Thinking] {
		problems = append(problems, "defaults.thinking is invalid")
	}
	seen := map[string]bool{}
	for _, agent := range c.Agents {
		if strings.TrimSpace(agent.Name) == "" {
			problems = append(problems, "agent name is required")
			continue
		}
		if seen[agent.Name] {
			problems = append(problems, "duplicate agent: "+agent.Name)
		}
		seen[agent.Name] = true
		if !validThinking[agent.Thinking] {
			problems = append(problems, agent.Name+" thinking is invalid")
		}
		if agent.Model == "" {
			problems = append(problems, agent.Name+" model is required")
		}
		if agent.PromptEngineering.System == "" || agent.PromptEngineering.User == "" {
			problems = append(problems, agent.Name+" prompt paths are required")
		}
		for _, p := range []string{agent.PromptEngineering.System, agent.PromptEngineering.User} {
			if p != "" {
				clean := filepath.Clean(p)
				if filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
					problems = append(problems, agent.Name+" prompt path escapes config directory: "+p)
					continue
				}
				if info, err := os.Stat(filepath.Join(base, clean)); err != nil || info.IsDir() {
					problems = append(problems, agent.Name+" prompt missing: "+p)
				}
			}
		}
	}
	if len(c.Pipelines) == 0 {
		problems = append(problems, "at least one pipeline is required")
	}
	defaultCount := 0
	pipelineNames := map[string]bool{}
	for _, pipeline := range c.Pipelines {
		if strings.TrimSpace(pipeline.Name) == "" || pipelineNames[pipeline.Name] {
			problems = append(problems, "pipeline names must be non-empty and unique")
		}
		pipelineNames[pipeline.Name] = true
		if pipeline.Default {
			defaultCount++
		}
		problems = append(problems, validatePipeline(pipeline, seen)...)
	}
	if defaultCount != 1 {
		problems = append(problems, "exactly one pipeline must be default")
	}
	return problems
}

func validatePipeline(pipeline Pipeline, agents map[string]bool) []string {
	var problems []string
	seenIDs := map[string]bool{}
	builds, verifies, reviews := 0, 0, 0
	for _, stage := range pipeline.Stages {
		if stage.ID == "" || seenIDs[stage.ID] {
			problems = append(problems, "pipeline "+pipeline.Name+" stage ids must be non-empty and unique")
		}
		seenIDs[stage.ID] = true
		switch stage.Kind {
		case "build":
			builds++
			if !agents[stage.Agent] {
				problems = append(problems, "pipeline "+pipeline.Name+" stage "+stage.ID+" references undefined agent "+stage.Agent)
			}
		case "verify":
			verifies++
			if stage.Agent != "" {
				problems = append(problems, "pipeline "+pipeline.Name+" verify stage "+stage.ID+" cannot name an agent")
			}
		case "review":
			reviews++
			if !agents[stage.Agent] {
				problems = append(problems, "pipeline "+pipeline.Name+" stage "+stage.ID+" references undefined agent "+stage.Agent)
			}
		default:
			problems = append(problems, "pipeline "+pipeline.Name+" stage "+stage.ID+" has invalid kind "+stage.Kind)
		}
	}
	if builds != 1 || verifies != 1 || reviews > 1 {
		problems = append(problems, "pipeline "+pipeline.Name+" must contain one build -> one verify -> optional review")
	}
	want := []string{"build", "verify"}
	if reviews == 1 {
		want = append(want, "review")
	}
	if len(pipeline.Stages) != len(want) {
		return problems
	}
	for index, stage := range pipeline.Stages {
		if stage.Kind != want[index] {
			problems = append(problems, fmt.Sprintf("pipeline %s stage order is invalid at index %d", pipeline.Name, index))
			break
		}
	}
	return problems
}

func (c Config) DefaultPipeline() (Pipeline, bool) {
	for _, pipeline := range c.Pipelines {
		if pipeline.Default {
			return pipeline, true
		}
	}
	return Pipeline{}, false
}

func (c Config) Pipeline(name string) (Pipeline, bool) {
	for _, pipeline := range c.Pipelines {
		if pipeline.Name == name {
			return pipeline, true
		}
	}
	return Pipeline{}, false
}

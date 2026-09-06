package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var validThinking = map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// validHarnesses lists registered coding agents. Only pi is implemented today;
// codex is reserved so task creation can offer it once its adapter lands.
var validHarnesses = map[string]bool{"pi": true, "codex": true}

// IsValidThinking reports whether level is an accepted thinking level.
func IsValidThinking(level string) bool { return validThinking[level] }

// ThinkingLevels returns accepted thinking levels in UI order.
func ThinkingLevels() []string { return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} }

// IsValidHarness reports whether name is a known coding agent.
func IsValidHarness(name string) bool { return validHarnesses[name] }

// HarnessNames returns known coding agents in UI order.
func HarnessNames() []string { return []string{"pi", "codex"} }

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
	System string `yaml:"system" json:"system"`
	User   string `yaml:"user" json:"user"`
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
		if agent.Name != "planner" && agent.Name != "builder" && agent.Name != "reviewer" {
			problems = append(problems, "unsupported agent: "+agent.Name)
		}
	}
	for _, role := range []string{"planner", "builder", "reviewer"} {
		found := false
		for _, a := range c.Agents {
			if a.Name == role {
				found = true
				if seen[role] {
					problems = append(problems, "duplicate agent: "+role)
				}
				seen[role] = true
				if !validThinking[a.Thinking] {
					problems = append(problems, role+" thinking is invalid")
				}
				if a.Model == "" {
					problems = append(problems, role+" model is required")
				}
				if a.PromptEngineering.System == "" || a.PromptEngineering.User == "" {
					problems = append(problems, role+" prompt paths are required")
				}
				for _, p := range []string{a.PromptEngineering.System, a.PromptEngineering.User} {
					if p != "" {
						clean := filepath.Clean(p)
						if filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
							problems = append(problems, role+" prompt path escapes config directory: "+p)
							continue
						}
						if info, err := os.Stat(filepath.Join(base, clean)); err != nil || info.IsDir() {
							problems = append(problems, role+" prompt missing: "+p)
						}
					}
				}
			}
		}
		if !found {
			problems = append(problems, "missing agent: "+role)
		}
	}
	return problems
}

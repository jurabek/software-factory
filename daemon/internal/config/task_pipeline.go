package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Freeze resolves the standard pipeline when none is declared and inlines
// prompt file contents so a task can persist a self-contained snapshot.
func Freeze(c Config, configPath string) (Config, error) {
	c = Fallback(c)
	base := filepath.Dir(configPath)
	for index := range c.Agents {
		agent := &c.Agents[index]
		if agent.PromptEngineering.SystemContent == "" && agent.PromptEngineering.System != "" && configPath != "" {
			body, err := os.ReadFile(filepath.Join(base, agent.PromptEngineering.System))
			if err != nil {
				return Config{}, fmt.Errorf("read %s system prompt: %w", agent.Name, err)
			}
			agent.PromptEngineering.SystemContent = string(body)
		}
		if agent.PromptEngineering.UserContent == "" && agent.PromptEngineering.User != "" && configPath != "" {
			body, err := os.ReadFile(filepath.Join(base, agent.PromptEngineering.User))
			if err != nil {
				return Config{}, fmt.Errorf("read %s user prompt: %w", agent.Name, err)
			}
			agent.PromptEngineering.UserContent = string(body)
		}
	}
	return c, nil
}

// SelectPipeline returns the named pipeline, or the default when name is empty.
func SelectPipeline(c Config, name string) (Pipeline, error) {
	if name == "" {
		pipeline, ok := c.DefaultPipeline()
		if !ok {
			return Pipeline{}, fmt.Errorf("configuration has no default pipeline")
		}
		return pipeline, nil
	}
	pipeline, ok := c.Pipeline(name)
	if !ok {
		return Pipeline{}, fmt.Errorf("unknown pipeline %q", name)
	}
	return pipeline, nil
}

// Snapshot encodes a config for durable task storage.
func Snapshot(c Config) (string, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Resolve returns a task's frozen configuration, falling back to the supplied
// base config when the task has no snapshot.
func Resolve(current Config, configPath, snapshot string) (Config, error) {
	if snapshot == "" {
		return current, nil
	}
	configured, problems, err := Parse([]byte(snapshot), filepath.Dir(configPath))
	if err != nil {
		return Config{}, err
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid task config: %s", strings.Join(problems, "; "))
	}
	return configured, nil
}

// TaskPipeline resolves a task's frozen config and selected pipeline.
func TaskPipeline(current Config, configPath, snapshot, name string) (Config, Pipeline, error) {
	c, err := Resolve(current, configPath, snapshot)
	if err != nil {
		return Config{}, Pipeline{}, err
	}
	c = Fallback(c)
	if len(c.Agents) == 0 {
		return c, Pipeline{}, nil
	}
	pipeline, ok := c.Pipeline(name)
	if !ok {
		pipeline, ok = c.DefaultPipeline()
	}
	if !ok {
		return Config{}, Pipeline{}, fmt.Errorf("task pipeline %q is unavailable", name)
	}
	return c, pipeline, nil
}

// Fallback supplies a fixed standard pipeline when a config declares agents
// but no pipelines. Order stays fixed here, not in configuration.
func Fallback(c Config) Config {
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
	stages := []Stage{
		{ID: "plan", Kind: "plan", Agent: agent("planner")},
		{ID: "build", Kind: "build", Agent: agent("builder")},
		{ID: "check", Kind: "verify"},
		{ID: "review", Kind: "review", Agent: agent("reviewer")},
	}
	return Config{
		Defaults:  c.Defaults,
		Runtime:   c.Runtime,
		Agents:    c.Agents,
		Pipelines: []Pipeline{{Name: "standard", Default: true, Stages: stages}},
	}
}

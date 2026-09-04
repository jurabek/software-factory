package config

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Model struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window,omitempty"`
}
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func Catalog(ctx context.Context, runner CommandRunner, piPath string) ([]Model, error) {
	out, err := runner.Run(ctx, piPath, "--list-models")
	if err != nil {
		return nil, fmt.Errorf("list pi models: %w", err)
	}
	var models []Model
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || !strings.Contains(fields[0], "/") {
			continue
		}
		parts := strings.SplitN(fields[0], "/", 2)
		window := 0
		for _, field := range fields[1:] {
			normalized := strings.TrimSuffix(strings.ToLower(strings.ReplaceAll(field, ",", "")), "k")
			if value, parseErr := strconv.Atoi(normalized); parseErr == nil {
				if strings.HasSuffix(strings.ToLower(field), "k") {
					value *= 1000
				}
				window = value
			}
		}
		models = append(models, Model{Provider: parts[0], ID: parts[1], ContextWindow: window})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	return models, nil
}

func ResolveModel(pattern string, models []Model) (Model, error) {
	matches := func(fn func(Model) bool) []Model {
		var result []Model
		for _, m := range models {
			if fn(m) {
				result = append(result, m)
			}
		}
		return result
	}
	if exact := matches(func(m Model) bool { return m.Provider+"/"+m.ID == pattern }); len(exact) == 1 {
		return exact[0], nil
	}
	if exact := matches(func(m Model) bool { return m.ID == pattern }); len(exact) == 1 {
		return exact[0], nil
	}
	if partial := matches(func(m Model) bool {
		return strings.Contains(m.ID, pattern) || strings.Contains(m.Provider+"/"+m.ID, pattern)
	}); len(partial) == 1 {
		return partial[0], nil
	} else if len(partial) > 1 {
		return Model{}, fmt.Errorf("ambiguous model %q", pattern)
	}
	return Model{}, fmt.Errorf("model %q not found", pattern)
}

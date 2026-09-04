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
		if model, ok := parseCatalogRow(strings.Fields(scanner.Text())); ok {
			models = append(models, model)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	return models, nil
}

func parseCatalogRow(fields []string) (Model, bool) {
	if len(fields) < 2 || fields[0] == "provider" {
		return Model{}, false
	}

	provider, id, contextIndex := fields[0], fields[1], 2
	if strings.Contains(fields[0], "/") {
		parts := strings.SplitN(fields[0], "/", 2)
		provider, id, contextIndex = parts[0], parts[1], 1
	}
	if provider == "" || id == "" {
		return Model{}, false
	}

	window := 0
	if contextIndex < len(fields) {
		window = parseTokenCount(fields[contextIndex])
	}
	return Model{Provider: provider, ID: id, ContextWindow: window}, true
}

func parseTokenCount(field string) int {
	normalized := strings.ToUpper(strings.ReplaceAll(field, ",", ""))
	multiplier := 1.0
	if strings.HasSuffix(normalized, "K") {
		normalized = strings.TrimSuffix(normalized, "K")
		multiplier = 1_000
	} else if strings.HasSuffix(normalized, "M") {
		normalized = strings.TrimSuffix(normalized, "M")
		multiplier = 1_000_000
	}
	value, err := strconv.ParseFloat(normalized, 64)
	if err != nil {
		return 0
	}
	return int(value*multiplier + 0.5)
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

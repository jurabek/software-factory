package config

import (
	"context"
	"testing"
)

type catalogRunner struct {
	output string
}

func (runner catalogRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(runner.output), nil
}

func TestCatalogParsesPiTable(t *testing.T) {
	t.Parallel()

	output := `provider model context max-out thinking images
openai-codex gpt-5.6-luna 272K 128K yes yes
openai-codex gpt-5.6-sol 272K 128K yes yes
`
	models, err := Catalog(context.Background(), catalogRunner{output: output}, "pi")
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("Catalog() returned %d models, want 2", len(models))
	}

	want := Model{Provider: "openai-codex", ID: "gpt-5.6-luna", ContextWindow: 272000}
	if models[0] != want {
		t.Errorf("Catalog() first model = %#v, want %#v", models[0], want)
	}
	if _, err := ResolveModel("openai-codex/gpt-5.6-luna", models); err != nil {
		t.Errorf("ResolveModel() error = %v", err)
	}
}

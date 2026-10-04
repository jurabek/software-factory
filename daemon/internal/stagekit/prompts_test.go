package stagekit

import (
	"strings"
	"testing"
)

func TestRenderPromptsDoesNotIncludeDataInRangeError(t *testing.T) {
	_, _, err := RenderPrompts("reviewer", "system", "", "{{range .Diff}}{{.}}{{end}}", "", map[string]any{
		"Diff": struct{ Patch string }{Patch: "private repository patch"},
	}, "", "instructions")
	if err == nil || !strings.Contains(err.Error(), "range target is not iterable") || !strings.Contains(err.Error(), "frozen-prompt:1:") {
		t.Fatalf("expected actionable template location, got %v", err)
	}
	if strings.Contains(err.Error(), "private repository patch") {
		t.Fatalf("template error included prompt data: %v", err)
	}
}

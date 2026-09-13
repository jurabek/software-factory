package factory

import (
	"strings"
	"testing"
)

func TestAgentEnvelopesRequireBoundedMarkdownReports(t *testing.T) {
	base := `"status":"success","summary":"ok","artifacts":[],"notes_for_next_agent":""`
	valid := `{` + base + `,"report_markdown":"# Report\n\nDone.","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := ValidatePlan(valid); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	missing := `{` + base + `,"steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := ValidatePlan(missing); err == nil {
		t.Fatal("missing report accepted")
	}
	oversized := `{` + base + `,"report_markdown":"` + strings.Repeat("x", 256<<10+1) + `","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := ValidatePlan(oversized); err == nil {
		t.Fatal("oversized report accepted")
	}
}

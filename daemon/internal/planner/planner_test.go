package planner

import (
	"strings"
	"testing"
)

func TestValidateAcceptsWellFormedPlan(t *testing.T) {
	payload := `{"status":"success","summary":"ok","notes_for_next_agent":"","report_markdown":"# Plan\n\nDone.","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := Validate(payload); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestValidateRejectsMissingReport(t *testing.T) {
	payload := `{"status":"success","summary":"ok","notes_for_next_agent":"","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("missing report accepted")
	}
}

func TestValidateRejectsOversizedReport(t *testing.T) {
	payload := `{"status":"success","summary":"ok","notes_for_next_agent":"","report_markdown":"` + strings.Repeat("x", 256<<10+1) + `","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("oversized report accepted")
	}
}

func TestValidateRejectsBlankQuestion(t *testing.T) {
	payload := `{"status":"success","summary":"ok","notes_for_next_agent":"","report_markdown":"# Plan","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]}],"questions":[" "]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("blank question accepted")
	}
}

func TestValidateRejectsDuplicateStep(t *testing.T) {
	payload := `{"status":"success","summary":"ok","notes_for_next_agent":"","report_markdown":"# Plan","steps":[{"id":"one","description":"do it","expected_files":[],"acceptance_criteria":[]},{"id":"one","description":"again","expected_files":[],"acceptance_criteria":[]}],"questions":[]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("duplicate step accepted")
	}
}

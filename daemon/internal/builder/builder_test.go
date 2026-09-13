package builder

import (
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"testing"
)

func TestValidateAcceptsWellFormedBuild(t *testing.T) {
	payload := `{"status":"success","summary":"built","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Build\n\nDone.","changed_files":["changed_test.go"],"commit_message":"test","test_changes":[{"path":"changed_test.go","reason":"adds the regression assertion"}]}`
	if _, err := Validate(payload); err != nil {
		t.Fatalf("valid build rejected: %v", err)
	}
}

func TestValidateRejectsMissingTestChanges(t *testing.T) {
	payload := `{"status":"success","summary":"built","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Build","changed_files":[],"commit_message":"test"}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("missing test_changes accepted")
	}
}

func TestValidateTestChangeSetRequiresExactGitDerivedSet(t *testing.T) {
	expected := map[string]factorygit.Change{"changed_test.go": {Path: "changed_test.go"}}
	valid := []TestChange{{Path: "changed_test.go", Reason: "covers the change"}}
	if err := ValidateTestChangeSet(valid, expected); err != nil {
		t.Fatalf("exact set rejected: %v", err)
	}
	if err := ValidateTestChangeSet(nil, expected); err == nil {
		t.Fatal("missing Git-derived test change accepted")
	}
	unknown := []TestChange{{Path: "other_test.go", Reason: "covers the change"}}
	if err := ValidateTestChangeSet(unknown, expected); err == nil {
		t.Fatal("unknown test change accepted")
	}
	duplicate := []TestChange{
		{Path: "changed_test.go", Reason: "one"},
		{Path: "changed_test.go", Reason: "two"},
	}
	if err := ValidateTestChangeSet(duplicate, expected); err == nil {
		t.Fatal("duplicate test change accepted")
	}
}

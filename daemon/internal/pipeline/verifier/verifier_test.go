package verifier

import (
	"strings"
	"testing"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestReportRendersCheckOutcomes(t *testing.T) {
	report, err := Report([]store.Check{
		{Name: "build-output", Command: "go test ./...", Status: "passed"},
		{Name: "lint", Command: "go vet ./...", Status: "failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "build-output") || !strings.Contains(report, "passed") {
		t.Fatalf("report missing passed check: %q", report)
	}
	if !strings.Contains(report, "lint") || !strings.Contains(report, "failed") {
		t.Fatalf("report missing failed check: %q", report)
	}
}

func TestComparisonPathsSkipsDeletions(t *testing.T) {
	entries := []ExpectedTestChange{
		{Change: factorygit.Change{Path: "added_test.go", Kind: "added"}},
		{Change: factorygit.Change{Path: "removed_test.go", Kind: "deleted"}},
	}
	paths := ComparisonPaths(entries)
	if len(paths) != 1 || paths[0] != "added_test.go" {
		t.Fatalf("overlay paths = %#v, want [added_test.go]", paths)
	}
}

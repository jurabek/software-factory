package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type emptyRunner struct{}

func (emptyRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestChangedFilesReturnsEmptySliceWhenRepositoryIsUnchanged(t *testing.T) {
	files, err := ChangedFiles(context.Background(), emptyRunner{}, "/repository", "base-sha")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if files == nil {
		t.Fatal("ChangedFiles() returned nil; want an empty slice for JSON arrays")
	}
	if len(files) != 0 {
		t.Fatalf("len(ChangedFiles()) = %d, want 0", len(files))
	}
}

type recordingRunner struct {
	commands [][]string
}

func (runner *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	return nil, nil
}

func TestChangedFilesUsesExplicitBaseAndPreservesFilenameWhitespace(t *testing.T) {
	runner := &recordingRunner{}
	_, err := ChangedFiles(context.Background(), runner, "/repository", "base-sha")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git", "-C", "/repository", "diff", "--name-only", "-z", "base-sha", "--"}
	if !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("diff command = %#v, want %#v", runner.commands[0], want)
	}
}

func TestMatchesGlobUsesRepositoryRelativeSegments(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		pattern string
		match   bool
	}{
		{name: "root test", file: "root_test.go", pattern: "**/*_test.go", match: true},
		{name: "nested test", file: "pkg/unit/root_test.go", pattern: "**/*_test.go", match: true},
		{name: "single star does not cross directories", file: "pkg/root_test.go", pattern: "*_test.go", match: false},
		{name: "test directory", file: "tests/fixtures/data.txt", pattern: "tests/**", match: true},
		{name: "empty pattern set", file: "root_test.go", pattern: "", match: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MatchesGlob(test.file, []string{test.pattern})
			if got != test.match {
				t.Fatalf("MatchesGlob(%q, %q) = %v, want %v", test.file, test.pattern, got, test.match)
			}
		})
	}
}

func TestValidateTestPatternRejectsAbsoluteAndEscapingPatterns(t *testing.T) {
	for _, pattern := range []string{"/tmp/*_test.go", "../*_test.go", "tests/../../*_test.go"} {
		if err := validateTestPattern(pattern); err == nil {
			t.Fatalf("validateTestPattern(%q) accepted an invalid pattern", pattern)
		}
	}
}

func TestDetectQualityProfileDefaultsAndPreservesExplicitEmptyTests(t *testing.T) {
	root := t.TempDir()
	_, _, _, tests, preChange, err := DetectQualityProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tests, defaultTestPatterns) || preChange {
		t.Fatalf("defaults = %#v, pre_change_verification = %v", tests, preChange)
	}
	content := "<!-- software-factory:start -->\n```yaml\ntests: []\npre_change_verification: true\n```\n<!-- software-factory:end -->"
	if err = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, tests, preChange, err = DetectQualityProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if tests == nil || len(tests) != 0 || !preChange {
		t.Fatalf("explicit profile = %#v, pre_change_verification = %v", tests, preChange)
	}
}

func TestCommitStagesOnlyLiteralPathsAndPreservesUnrelatedChanges(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "target.txt"), "one")
	writeFile(t, filepath.Join(root, "unrelated.txt"), "one")
	runGit(t, root, "add", "--", "target.txt", "unrelated.txt")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, "target.txt"), "two")
	writeFile(t, filepath.Join(root, "unrelated.txt"), "two")
	runGit(t, root, "add", "--", "unrelated.txt")

	sha, err := Commit(context.Background(), OSRunner{}, root, "factory update", []string{"target.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(runGit(t, root, "show", "--format=%s", "--no-patch", sha)) != "factory update" {
		t.Fatalf("commit message mismatch")
	}
	if got := strings.TrimSpace(runGit(t, root, "show", "--format=", "--name-only", sha)); got != "target.txt" {
		t.Fatalf("committed paths = %q, want target.txt", got)
	}
	if got := strings.TrimSpace(runGit(t, root, "diff", "--cached", "--name-only")); got != "unrelated.txt" {
		t.Fatalf("unrelated staged paths = %q, want unrelated.txt", got)
	}
}

func TestCommitForceAddsIgnoredPathAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, ".gitignore"), ".software-factory/\n")
	writeFile(t, filepath.Join(root, "README"), "initial")
	runGit(t, root, "add", ".gitignore", "README")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, ".software-factory", "plan.md"), "plan")
	if _, err := Commit(context.Background(), OSRunner{}, root, "publish", []string{".software-factory/plan.md"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, root, "show", "--format=", "--name-only", "HEAD")); got != ".software-factory/plan.md" {
		t.Fatalf("ignored committed path = %q", got)
	}
	if _, err := Commit(context.Background(), OSRunner{}, root, "escape", []string{"../outside"}); err == nil {
		t.Fatal("escaping path accepted")
	}
	if _, err := Commit(context.Background(), OSRunner{}, root, "magic", []string{".:(glob)**"}); err == nil {
		t.Fatal("Git pathspec expression accepted")
	}
}

func TestCommitNoOpIgnoresUnrelatedStagedChanges(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "target.txt"), "same")
	writeFile(t, filepath.Join(root, "unrelated.txt"), "before")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, "unrelated.txt"), "after")
	runGit(t, root, "add", "--", "unrelated.txt")
	before := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	got, err := Commit(context.Background(), OSRunner{}, root, "no-op", []string{"target.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != before || strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")) != before {
		t.Fatalf("no-op changed HEAD from %s to %s", before, got)
	}
	if staged := strings.TrimSpace(runGit(t, root, "diff", "--cached", "--name-only")); staged != "unrelated.txt" {
		t.Fatalf("unrelated staging = %q", staged)
	}
}

func TestFingerprintDetectsContentModeSymlinkAndStagedChanges(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one")
	writeFile(t, filepath.Join(root, "staged.txt"), "one")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	writeFile(t, filepath.Join(root, "dirty.txt"), "one")
	before, err := Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "dirty.txt"), "two")
	after, err := Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil || before == after {
		t.Fatalf("content fingerprint = %q -> %q, err=%v", before, after, err)
	}
	before = after
	if err = os.Chmod(filepath.Join(root, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err = Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil || before == after {
		t.Fatalf("mode fingerprint did not change")
	}
	if err = os.Symlink("dirty.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	after, err = Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("tracked.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	latest, err := Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil || after == latest {
		t.Fatalf("symlink fingerprint did not change")
	}
	runGit(t, root, "add", "--", "staged.txt")
	writeFile(t, filepath.Join(root, "staged.txt"), "working")
	staged, err := Fingerprint(context.Background(), OSRunner{}, root)
	if err != nil || latest == staged {
		t.Fatalf("staged fingerprint did not change")
	}
}

func TestPrepareLocalUsesDistinctNamedBranches(t *testing.T) {
	source := t.TempDir()
	runGit(t, source, "init")
	writeFile(t, filepath.Join(source, "README"), "initial")
	runGit(t, source, "add", "README")
	runGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	firstDestination := filepath.Join(t.TempDir(), "first")
	secondDestination := filepath.Join(t.TempDir(), "second")
	first, err := PrepareLocal(context.Background(), OSRunner{}, source, firstDestination)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareLocal(context.Background(), OSRunner{}, source, secondDestination)
	if err != nil {
		t.Fatal(err)
	}
	if first.BranchName == "" || first.BranchName == second.BranchName {
		t.Fatalf("branches = %q and %q", first.BranchName, second.BranchName)
	}
	if branch := strings.TrimSpace(runGit(t, firstDestination, "symbolic-ref", "--short", "HEAD")); branch != first.BranchName {
		t.Fatalf("first branch = %q, want %q", branch, first.BranchName)
	}
	if branch := strings.TrimSpace(runGit(t, secondDestination, "symbolic-ref", "--short", "HEAD")); branch != second.BranchName {
		t.Fatalf("second branch = %q, want %q", branch, second.BranchName)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

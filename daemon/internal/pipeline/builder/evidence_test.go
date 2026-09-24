package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestValidateWithEvidenceRequiresExactlyGitDerivedTestChanges(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	evidenceGit(t, repositoryPath, "init")
	evidenceWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "package example\n")
	evidenceGit(t, repositoryPath, "add", ".")
	evidenceGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(evidenceGit(t, repositoryPath, "rev-parse", "HEAD"))
	evidenceWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "package example\nfunc TestChanged(t *testing.T) {}\n")

	valid := `{"status":"success","summary":"built","notes_for_next_agent":"","report_markdown":"# Build\n\nDone.","changed_files":["changed_test.go"],"commit_message":"test","test_changes":[{"path":"changed_test.go","reason":"adds the regression assertion"}]}`
	if _, err := ValidateWithEvidence(repositoryPath, base, []string{"**/*_test.go"}, valid); err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(valid, `[{"path":"changed_test.go","reason":"adds the regression assertion"}]`, `[]`, 1)
	if _, err := ValidateWithEvidence(repositoryPath, base, []string{"**/*_test.go"}, missing); err == nil {
		t.Fatal("missing Git-derived test change accepted")
	}
	unknown := strings.Replace(valid, `{"path":"changed_test.go","reason":"adds the regression assertion"}`, `{"path":"other_test.go","reason":"adds the regression assertion"}`, 1)
	if _, err := ValidateWithEvidence(repositoryPath, base, []string{"**/*_test.go"}, unknown); err == nil {
		t.Fatal("unknown repository test change accepted")
	}
}

func TestPersistEvidenceRetainsChangeKindAndReason(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	evidenceGit(t, repositoryPath, "init")
	evidenceWrite(t, filepath.Join(repositoryPath, "example_test.go"), "package example\n")
	evidenceGit(t, repositoryPath, "add", ".")
	evidenceGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(evidenceGit(t, repositoryPath, "rev-parse", "HEAD"))
	evidenceWrite(t, filepath.Join(repositoryPath, "example_test.go"), "package example\nfunc TestExample(t *testing.T) {}\n")
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := evidenceTask(t, db, root, repositoryPath, base)
	profileBody := `{"root":"","base_sha":"` + base + `","tests":["**/*_test.go"]}`
	if err = os.WriteFile(filepath.Join(task.WorkspacePath, "repository-profile.json"), []byte(profileBody), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"status":"success","summary":"built","notes_for_next_agent":"","report_markdown":"# Build\n\nDone.","changed_files":["example_test.go"],"commit_message":"test","test_changes":[{"path":"example_test.go","reason":"covers the changed behavior"}]}`
	if err = PersistEvidence(context.Background(), db, task, store.Phase{ID: "build-attempt", Attempt: 1}, payload); err != nil {
		t.Fatal(err)
	}
	changes, err := db.Evidence.TestChanges(context.Background(), task.ID)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %#v, err = %v", changes, err)
	}
	if changes[0].ChangeKind != "modified" || changes[0].Reason != "covers the changed behavior" {
		t.Fatalf("stored evidence = %#v", changes[0])
	}
}

func evidenceTask(t *testing.T, db *store.Store, root, repositoryPath, base string) store.Task {
	t.Helper()
	task := store.Task{ID: "task-1", Request: "evidence", WorkspacePath: filepath.Join(root, "task"), RepositoryType: "local", RepositorySource: repositoryPath, RepositoryPath: repositoryPath, BaseSHA: base, ReviewBaseSHA: base, State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := db.Tasks.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func evidenceGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func evidenceWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestBuilderValidatorRequiresExactlyGitDerivedTestChanges(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	qualityGit(t, repositoryPath, "init")
	qualityWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "package example\n")
	qualityGit(t, repositoryPath, "add", ".")
	qualityGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(qualityGit(t, repositoryPath, "rev-parse", "HEAD"))
	qualityWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "package example\nfunc TestChanged(t *testing.T) {}\n")

	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := qualityTask(t, db, root, store.TaskRepository{ID: "repo-1", TaskID: "task-1", Name: "app", WorkingPath: repositoryPath, BaseSHA: base, ReviewBaseSHA: base})
	service := NewService(root, Dependencies{Store: db, Config: configForQuality(), Git: factorygit.OSRunner{}})
	profiles := map[string]Materialization{"app": {Tests: []string{"**/*_test.go"}}}
	validate := service.builderValidator(context.Background(), task, profiles)
	valid := `{"status":"success","summary":"built","artifacts":[],"notes_for_next_agent":"","changed_files":["changed_test.go"],"commit_message":"test","test_changes":[{"repository_id":"repo-1","path":"changed_test.go","reason":"adds the regression assertion"}]}`
	if _, err = validate(valid); err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(valid, `[{"repository_id":"repo-1","path":"changed_test.go","reason":"adds the regression assertion"}]`, `[]`, 1)
	if _, err = validate(missing); err == nil {
		t.Fatal("missing Git-derived test change accepted")
	}
	unknown := strings.Replace(valid, `"repository_id":"repo-1"`, `"repository_id":"other"`, 1)
	if _, err = validate(unknown); err == nil {
		t.Fatal("unknown repository test change accepted")
	}
}

func TestPersistBuilderEvidenceRetainsChangeKindAndReason(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	qualityGit(t, repositoryPath, "init")
	qualityWrite(t, filepath.Join(repositoryPath, "example_test.go"), "package example\n")
	qualityGit(t, repositoryPath, "add", ".")
	qualityGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(qualityGit(t, repositoryPath, "rev-parse", "HEAD"))
	qualityWrite(t, filepath.Join(repositoryPath, "example_test.go"), "package example\nfunc TestExample(t *testing.T) {}\n")
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := qualityTask(t, db, root, store.TaskRepository{ID: "repo-1", TaskID: "task-1", Name: "app", WorkingPath: repositoryPath, BaseSHA: base, ReviewBaseSHA: base})
	service := NewService(root, Dependencies{Store: db, Config: configForQuality(), Git: factorygit.OSRunner{}})
	profileDir := filepath.Join(task.WorkspacePath, "repository-profiles")
	if err = os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profileBody := `{"root":"","base_sha":"` + base + `","tests":["**/*_test.go"]}`
	if err = os.WriteFile(filepath.Join(profileDir, "app.json"), []byte(profileBody), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"status":"success","summary":"built","artifacts":[],"notes_for_next_agent":"","changed_files":["example_test.go"],"commit_message":"test","test_changes":[{"repository_id":"repo-1","path":"example_test.go","reason":"covers the changed behavior"}]}`
	if err = service.persistBuilderEvidence(context.Background(), task, store.Phase{ID: "build-attempt", Attempt: 1}, payload); err != nil {
		t.Fatal(err)
	}
	changes, err := db.TestChanges(context.Background(), task.ID)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %#v, err = %v", changes, err)
	}
	if changes[0].ChangeKind != "modified" || changes[0].Reason != "covers the changed behavior" || changes[0].RepositoryID != "repo-1" {
		t.Fatalf("stored evidence = %#v", changes[0])
	}
}

func TestCheckCancellationKillsProcessGroupAndPersistsCancelledRecord(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repositoryPath := filepath.Join(root, "repository")
	if err = os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	task := qualityTask(t, db, root, store.TaskRepository{ID: "repo-1", TaskID: "task-1", Name: "app", WorkingPath: repositoryPath})
	service := NewService(root, Dependencies{Store: db, Config: configForQuality(), Git: factorygit.OSRunner{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- service.runChecks(ctx, task, store.Phase{ID: "check-attempt", Name: "check", Attempt: 1}, task.Repositories[0], []Check{{ID: "sleep", Command: "sleep 30"}}, "primary", "")
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("check process did not terminate after cancellation")
	}
	checks, err := db.Checks(context.Background(), task.ID)
	if err != nil || len(checks) != 1 || checks[0].Status != "cancelled" {
		t.Fatalf("checks = %#v, err = %v", checks, err)
	}
}

func qualityTask(t *testing.T, db *store.DB, root string, repository store.TaskRepository) store.Task {
	t.Helper()
	task := store.Task{ID: "task-1", Request: "quality", WorkspacePath: filepath.Join(root, "task"), State: string(Preparing), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Repositories: []store.TaskRepository{repository}}
	if err := os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func configForQuality() config.Config { return config.Config{} }

func qualityGit(t *testing.T, directory string, args ...string) string {
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

func qualityWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

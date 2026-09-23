package verifier

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
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

func verifierTestKit(t *testing.T, root string) (*stagekit.Kit, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return stagekit.New(db, nil, nil, config.Config{}, "", root), db
}

func verifierTask(t *testing.T, db *store.Store, root, repositoryPath, base string) store.Task {
	t.Helper()
	task := store.Task{ID: "task-1", Request: "quality", WorkspacePath: filepath.Join(root, "task"), RepositoryType: "local", RepositorySource: repositoryPath, RepositoryPath: repositoryPath, BaseSHA: base, ReviewBaseSHA: base, State: string(stagekit.Preparing), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := db.Tasks.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCheckCancellationKillsProcessGroupAndPersistsCancelledRecord(t *testing.T) {
	root := t.TempDir()
	kit, db := verifierTestKit(t, root)
	repositoryPath := filepath.Join(root, "repository")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	task := verifierTask(t, db, root, repositoryPath, "")
	service := service{kit: kit}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- service.runChecks(ctx, task, store.Phase{ID: "check-attempt", Name: "check", Attempt: 1}, []workspace.Check{{ID: "sleep", Command: "sleep 30"}}, "primary", "")
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("check process did not terminate after cancellation")
	}
	checks, err := db.Checks.List(context.Background(), task.ID)
	if err != nil || len(checks) != 1 || checks[0].Status != "cancelled" {
		t.Fatalf("checks = %#v, err = %v", checks, err)
	}
}

func TestComparisonFailureIsPersistedAsAdvisoryObservation(t *testing.T) {
	root := t.TempDir()
	kit, db := verifierTestKit(t, root)
	repositoryPath := filepath.Join(root, "task", "workspace", "repository")
	verifierGit(t, repositoryPath, "init")
	verifierWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "base\n")
	verifierGit(t, repositoryPath, "add", ".")
	verifierGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	base := strings.TrimSpace(verifierGit(t, repositoryPath, "rev-parse", "HEAD"))
	task := verifierTask(t, db, root, repositoryPath, base)
	service := service{kit: kit}
	digest, err := kit.CaptureSnapshot(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Phases.Add(context.Background(), store.Phase{ID: "build-attempt", TaskID: task.ID, Sequence: 1, Name: "build", Kind: "build", Status: "success", Attempt: 1, BranchID: "branch", InputSnapshot: digest}); err != nil {
		t.Fatal(err)
	}
	verifierWrite(t, filepath.Join(repositoryPath, "changed_test.go"), "changed\n")
	verify := store.Phase{ID: "verify-attempt", TaskID: task.ID, Sequence: 2, Name: "check", Kind: "verify", Status: "running", Attempt: 1, BranchID: "branch"}
	profile := workspace.Materialization{
		Tests:                 []string{"**/*_test.go"},
		Checks:                []workspace.Check{{ID: "behavior", Command: `test "$(cat changed_test.go)" = "base"`}},
		PreChangeVerification: true,
	}
	if err = service.runComparisons(context.Background(), task, verify, profile); err != nil {
		t.Fatal(err)
	}
	comparisons, err := db.Evidence.Comparisons(context.Background(), task.ID)
	if err != nil || len(comparisons) != 1 {
		t.Fatalf("comparisons = %#v, err = %v", comparisons, err)
	}
	if comparisons[0].Status != "overlay_checks_failed" || comparisons[0].BaselineSnapshot != digest || len(comparisons[0].OverlayPaths) != 1 {
		t.Fatalf("comparison = %#v", comparisons[0])
	}
	checks, err := db.Checks.List(context.Background(), task.ID)
	if err != nil || len(checks) != 2 {
		t.Fatalf("checks = %#v, err = %v", checks, err)
	}
	if checks[0].Phase != "baseline" || checks[1].Phase != "test_overlay" || checks[0].Status != "passed" || checks[1].Status != "failed" {
		t.Fatalf("comparison check phases = %#v", checks)
	}
}

func verifierGit(t *testing.T, directory string, args ...string) string {
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

func verifierWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

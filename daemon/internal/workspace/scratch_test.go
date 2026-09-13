package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

func TestMaterializeScratchPreservesModesSymlinksAndIsolation(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspacePath := filepath.Join(root, "task")
	repositoryPath := filepath.Join(workspacePath, "workspace", "repository")
	scratchGit(t, repositoryPath, "init")
	scratchWrite(t, filepath.Join(repositoryPath, "run.sh"), "#!/bin/sh\necho ok\n")
	if err = os.Chmod(filepath.Join(repositoryPath, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	scratchWrite(t, filepath.Join(repositoryPath, "target.txt"), "target\n")
	if err = os.Symlink("target.txt", filepath.Join(repositoryPath, "target-link")); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, repositoryPath, "add", ".")
	scratchGit(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	task := scratchTask(t, db, root, repositoryPath, "")
	service := New(db, factorygit.OSRunner{})
	snapshot, err := service.CaptureSnapshot(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(root, "baseline")
	if err = service.MaterializeScratch(context.Background(), task, snapshot.Digest, baseline); err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(filepath.Join(baseline, "run.sh")); statErr != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("scratch executable = %v, err = %v", info, statErr)
	}
	if link, readErr := os.Readlink(filepath.Join(baseline, "target-link")); readErr != nil || link != "target.txt" {
		t.Fatalf("scratch symlink = %q, err = %v", link, readErr)
	}
	if info, statErr := os.Stat(filepath.Join(baseline, ".git")); statErr != nil || !info.IsDir() {
		t.Fatalf("scratch Git metadata = %v, err = %v", info, statErr)
	}
	scratchWrite(t, filepath.Join(baseline, "baseline-side-effect"), "must not leak")
	overlay := filepath.Join(root, "overlay")
	if err = service.MaterializeScratch(context.Background(), task, snapshot.Digest, overlay); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(overlay, "baseline-side-effect")); !os.IsNotExist(statErr) {
		t.Fatalf("baseline side effect leaked into overlay: %v", statErr)
	}
}

func scratchTask(t *testing.T, db *store.DB, root, repositoryPath, base string) store.Task {
	t.Helper()
	task := store.Task{ID: "task-1", Request: "scratch", WorkspacePath: filepath.Join(root, "task"), RepositoryType: "local", RepositorySource: repositoryPath, RepositoryPath: repositoryPath, BaseSHA: base, ReviewBaseSHA: base, State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := os.MkdirAll(task.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func scratchGit(t *testing.T, directory string, args ...string) string {
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

func scratchWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

type localWorkspaceSandbox struct {
	runner factorygit.Runner
}

func (sandbox localWorkspaceSandbox) Materialize(ctx context.Context, request MaterializationRequest) (Materialization, error) {
	profile, err := factorygit.PrepareLocal(ctx, sandbox.runner, request.Source, request.Destination)
	if err != nil {
		return Materialization{}, err
	}
	return Materialization{
		Root:         profile.Root,
		SourceType:   profile.SourceType,
		Source:       profile.Source,
		BaseSHA:      profile.BaseSHA,
		BranchName:   profile.BranchName,
		Checks:       convertChecks(profile.Checks),
		Generated:    profile.Generated,
		Protected:    profile.Protected,
		Tests:        profile.Tests,
		Instructions: profile.Instructions,
	}, nil
}

func (sandbox localWorkspaceSandbox) Cleanup(ctx context.Context, request CleanupRequest) error {
	for _, repository := range request.Repositories {
		if repository.SourceType != "local" || repository.WorkingPath == "" {
			continue
		}
		if _, err := os.Stat(repository.WorkingPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := sandbox.runner.Run(ctx, "git", "-C", repository.CanonicalPath, "worktree", "remove", "--force", repository.WorkingPath); err != nil {
			return err
		}
	}
	return nil
}

func convertChecks(checks []factorygit.Check) []Check {
	values := make([]Check, len(checks))
	for index, check := range checks {
		values[index] = Check{ID: check.ID, Command: check.Command}
	}
	return values
}

func TestWorkspacePreparationRecoversAfterSecondRepositoryFails(t *testing.T) {
	root := t.TempDir()
	firstSource := newLocalRepository(t, filepath.Join(root, "first-source"), "first.txt", "first")
	secondSource := filepath.Join(root, "second-source")
	if err := os.MkdirAll(secondSource, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sandbox := localWorkspaceSandbox{runner: factorygit.OSRunner{}}
	service := NewService(root, Dependencies{Store: db, Sandbox: sandbox, Git: factorygit.OSRunner{}})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "prepare two repositories", Repositories: []Repository{
		{Name: "first", Type: "local", Path: firstSource, Primary: true},
		{Name: "second", Type: "local", Path: secondSource},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = service.workspace.Prepare(context.Background(), task); err == nil {
		t.Fatal("expected the invalid second repository to fail preparation")
	}
	prepared, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Repositories[0].WorkingPath == "" || prepared.Repositories[1].WorkingPath != "" {
		t.Fatalf("partial preparation = %#v", prepared.Repositories)
	}

	newLocalRepository(t, secondSource, "second.txt", "second")
	if _, err = service.workspace.Prepare(context.Background(), prepared); err != nil {
		t.Fatalf("recovered preparation failed: %v", err)
	}
	recovered, err := db.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range recovered.Repositories {
		if repository.WorkingPath == "" || repository.BaseSHA == "" || repository.BranchName == "" {
			t.Fatalf("repository was not durably prepared: %+v", repository)
		}
		if _, err := os.Stat(repository.WorkingPath); err != nil {
			t.Fatalf("materialized repository %s is unavailable: %v", repository.Name, err)
		}
	}
	operations, err := db.WorkspaceOperations(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 4 {
		t.Fatalf("workspace operations = %#v, want initial success, failure, and recovery", operations)
	}
	if operations[0].Kind != "allocate" || operations[0].Status != "succeeded" || operations[1].Kind != "materialize" || operations[1].Status != "succeeded" || operations[2].Status != "failed" || operations[3].Kind != "materialize" || operations[3].Status != "succeeded" {
		t.Fatalf("workspace operation recovery = %#v", operations)
	}
}

func TestWorkspaceRestoreRepeatsAfterInterruptedGitOperation(t *testing.T) {
	root := t.TempDir()
	source := newLocalRepository(t, filepath.Join(root, "source"), "README", "initial")
	databasePath := filepath.Join(root, "factory.db")
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := localWorkspaceSandbox{runner: factorygit.OSRunner{}}
	service := NewService(root, Dependencies{Store: db, Sandbox: sandbox, Git: factorygit.OSRunner{}})
	task, err := service.tasks.create(context.Background(), CreateRequest{Request: "retry repository", Repositories: []Repository{{Name: "app", Type: "local", Path: source, Primary: true}}}, "")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = service.workspace.Prepare(context.Background(), task); err != nil {
		db.Close()
		t.Fatal(err)
	}
	task, err = db.Task(context.Background(), task.ID)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	snapshot, err := service.workspace.CaptureSnapshot(context.Background(), task)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	phase := store.Phase{ID: "attempt-1", TaskID: task.ID, Sequence: 1, Name: "building", Kind: "agent", Owner: "builder", Status: "failed", Attempt: 1, InputSnapshot: snapshot.Digest}
	if err = db.AddPhase(context.Background(), phase); err != nil {
		db.Close()
		t.Fatal(err)
	}
	repository := task.Repositories[0]
	if err = db.SavePhaseRepositoryInputs(context.Background(), phase.ID, []store.PhaseRepositoryInput{{PhaseID: phase.ID, RepositoryID: repository.ID, ReviewBaseSHA: repository.BaseSHA, HeadSHA: repository.BaseSHA, BranchName: repository.BranchName}}); err != nil {
		db.Close()
		t.Fatal(err)
	}

	failingStore := &failWorkspaceCompletionStore{DB: db}
	interruptedWorkspace := &workspaceService{root: root, db: failingStore, sandbox: sandbox, snapshots: &snapshotService{db: db, git: factorygit.OSRunner{}}, git: factorygit.OSRunner{}}
	if _, err = interruptedWorkspace.Restore(context.Background(), task, phase, "retry-once"); err == nil {
		db.Close()
		t.Fatal("expected interruption while settling restore operation")
	}
	interruptedPath := task.Repositories[0].WorkingPath
	if branch := gitBranch(t, interruptedPath); !strings.HasPrefix(branch, "software-factory/retry/") {
		db.Close()
		t.Fatalf("partial restore branch = %q", branch)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredDB, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredDB.Close()
	recoveredTask, err := recoveredDB.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredPhase, err := recoveredDB.PhaseByID(context.Background(), task.ID, phase.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredWorkspace := &workspaceService{root: root, db: recoveredDB, sandbox: sandbox, snapshots: &snapshotService{db: recoveredDB, git: factorygit.OSRunner{}}, git: factorygit.OSRunner{}}
	if _, err = recoveredWorkspace.Restore(context.Background(), recoveredTask, recoveredPhase, "retry-once"); err != nil {
		t.Fatal(err)
	}
	if head := gitHead(t, interruptedPath); head != repository.BaseSHA {
		t.Fatalf("restored head = %q, want %q", head, repository.BaseSHA)
	}
	if body, readErr := os.ReadFile(filepath.Join(interruptedPath, "README")); readErr != nil || string(body) != "initial" {
		t.Fatalf("restored content = %q, err = %v", body, readErr)
	}
	operations, err := recoveredDB.WorkspaceOperations(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, succeeded := false, false
	for _, operation := range operations {
		if operation.Kind == "restore" && operation.Status == "interrupted" {
			interrupted = true
		}
		if operation.Kind == "restore_snapshot" && operation.Status == "succeeded" {
			succeeded = true
		}
	}
	if !interrupted || !succeeded {
		t.Fatalf("restart recovery operations = %#v", operations)
	}
}

type failWorkspaceCompletionStore struct {
	*store.DB
	failed bool
}

func (db *failWorkspaceCompletionStore) UpdateWorkspaceOperation(ctx context.Context, id, status, operationError string) error {
	if status == "succeeded" && !db.failed {
		db.failed = true
		return errors.New("injected workspace completion failure")
	}
	return db.DB.UpdateWorkspaceOperation(ctx, id, status, operationError)
}

func newLocalRepository(t *testing.T, root, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "init")
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "add", "--", name)
	runGitCommand(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	return root
}

func runGitCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func gitHead(t *testing.T, root string) string {
	return strings.TrimSpace(runGitCommand(t, root, "rev-parse", "HEAD"))
}

func gitBranch(t *testing.T, root string) string {
	return strings.TrimSpace(runGitCommand(t, root, "symbolic-ref", "--short", "HEAD"))
}

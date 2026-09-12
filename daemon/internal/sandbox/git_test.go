package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/factory"
)

type recordingRunner struct {
	calls []string
	errAt int
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+join(args))
	if r.errAt > 0 && len(r.calls) == r.errAt {
		return nil, errors.New("runner failure")
	}
	return nil, nil
}

func join(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += " "
		}
		result += value
	}
	return result
}

func TestGitCleanupUsesTaskOwnedRepositoriesInOrder(t *testing.T) {
	runner := &recordingRunner{}
	adapter := Git{Runner: runner}
	err := adapter.Cleanup(context.Background(), factory.CleanupRequest{
		TaskID: "task-1", WorkspaceRoot: "/tasks/task-1",
		Repositories: []factory.CleanupRepository{
			{RepositoryID: "repo-1", Name: "one", SourceType: "local", CanonicalPath: "/repos/one", WorkingPath: "/tasks/task-1/workspace/repositories/one"},
			{RepositoryID: "repo-2", Name: "two", SourceType: "local", CanonicalPath: "/repos/two", WorkingPath: "/tasks/task-1/workspace/repositories/two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git -C /repos/one worktree remove --force /tasks/task-1/workspace/repositories/one",
		"git -C /repos/two worktree remove --force /tasks/task-1/workspace/repositories/two",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestGitCleanupStopsOnFailure(t *testing.T) {
	runner := &recordingRunner{errAt: 1}
	err := (Git{Runner: runner}).Cleanup(context.Background(), factory.CleanupRequest{
		TaskID: "task-1", WorkspaceRoot: "/tasks/task-1",
		Repositories: []factory.CleanupRepository{
			{RepositoryID: "repo-1", Name: "one", SourceType: "local", CanonicalPath: "/repos/one", WorkingPath: "/tasks/task-1/workspace/repositories/one"},
			{RepositoryID: "repo-2", Name: "two", SourceType: "local", CanonicalPath: "/repos/two", WorkingPath: "/tasks/task-1/workspace/repositories/two"},
		},
	})
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v, want one call", runner.calls)
	}
}

func TestGitCleanupRejectsForeignSandbox(t *testing.T) {
	err := (Git{Runner: &recordingRunner{}}).Cleanup(context.Background(), factory.CleanupRequest{
		TaskID: "task-1", WorkspaceRoot: "/tasks/task-1",
		Repositories: []factory.CleanupRepository{{
			RepositoryID: "repo-1", Name: "one", SourceType: "local", CanonicalPath: "/repos/one", WorkingPath: "/tasks/task-2/workspace/repositories/one",
		}},
	})
	if err == nil {
		t.Fatal("expected ownership failure")
	}
}

func TestGitCleanupRejectsTaskIDPrefixCollision(t *testing.T) {
	err := (Git{Runner: &recordingRunner{}}).Cleanup(context.Background(), factory.CleanupRequest{
		TaskID: "task-1", WorkspaceRoot: "/tasks/task-1",
		Repositories: []factory.CleanupRepository{{
			RepositoryID: "repo-1", Name: "one", SourceType: "local", CanonicalPath: "/repos/one", WorkingPath: "/tasks/task-10/workspace/repositories/one",
		}},
	})
	if err == nil {
		t.Fatal("expected ownership failure")
	}
}

func TestGitMaterializePropagatesLocalProfileAndTaskIsolation(t *testing.T) {
	source := t.TempDir()
	runner := &materializeRunner{sourceRoot: source}
	adapter := Git{Runner: runner}
	firstDestination := filepath.Join(t.TempDir(), "workspace", "repositories", "app")
	secondDestination := filepath.Join(t.TempDir(), "workspace", "repositories", "app")
	request := factory.MaterializationRequest{TaskID: "task-1", RepositoryID: "repo-1", Name: "app", SourceType: "local", Source: source, Destination: firstDestination}
	first, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.TaskID, request.Destination = "task-2", secondDestination
	second, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceType != "local" || first.Source != source || first.BaseSHA != "local-sha" {
		t.Fatalf("local profile = %#v", first)
	}
	if first.BranchName == second.BranchName {
		t.Fatalf("task branches = %q, want isolation", first.BranchName)
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root != resolvedSource || second.Root != resolvedSource {
		t.Fatalf("roots = %q, %q", first.Root, second.Root)
	}
}

func TestGitMaterializePropagatesGitHubProfileAndErrorsBySource(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "workspace", "repositories", "app")
	runner := &materializeRunner{}
	profile, err := (Git{Runner: runner}).Materialize(context.Background(), factory.MaterializationRequest{TaskID: "task-1", RepositoryID: "repo-1", Name: "app", SourceType: "github", Source: "owner/app", Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	if profile.SourceType != "github" || profile.Source != "owner/app" || profile.BaseSHA != "github-sha" || profile.Root != destination {
		t.Fatalf("github profile = %#v", profile)
	}
	for _, test := range []struct {
		name       string
		sourceType string
		source     string
		want       string
	}{
		{name: "local", sourceType: "local", source: "/missing", want: "materialize local repository"},
		{name: "github", sourceType: "github", source: "owner/app", want: "materialize github repository"},
	} {
		t.Run(test.name, func(t *testing.T) {
			failed := &materializeRunner{sourceRoot: sourceOrMissing(test.sourceType), fail: test.sourceType}
			_, err := (Git{Runner: failed}).Materialize(context.Background(), factory.MaterializationRequest{TaskID: "task-1", RepositoryID: "repo-1", Name: "app", SourceType: test.sourceType, Source: test.source, Destination: filepath.Join(t.TempDir(), "app")})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want source-specific context", err)
			}
		})
	}
}

func sourceOrMissing(sourceType string) string {
	if sourceType == "local" {
		return "/missing"
	}
	return ""
}

type materializeRunner struct {
	sourceRoot string
	fail       string
}

func (r *materializeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if r.fail == "local" && name == "git" && len(args) > 3 && args[2] == "rev-parse" {
		return nil, errors.New("local runner failure")
	}
	if r.fail == "github" && name == "gh" {
		return nil, errors.New("github runner failure")
	}
	if name == "gh" {
		if err := os.MkdirAll(args[len(args)-1], 0o755); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if name == "git" && len(args) >= 4 && args[2] == "rev-parse" {
		if args[3] == "--show-toplevel" {
			return []byte(r.sourceRoot), nil
		}
		if r.sourceRoot != "" {
			return []byte("local-sha\n"), nil
		}
		return []byte("github-sha\n"), nil
	}
	if name == "git" && len(args) >= 5 && args[2] == "worktree" {
		return []byte(""), os.MkdirAll(args[6], 0o755)
	}
	if name == "git" && len(args) >= 4 && args[2] == "switch" {
		return nil, nil
	}
	return nil, nil
}

func TestGitMaterializeRequiresIdentity(t *testing.T) {
	_, err := (Git{}).Materialize(context.Background(), factory.MaterializationRequest{Destination: "/tmp/repository"})
	if err == nil {
		t.Fatal("expected identity validation failure")
	}
}

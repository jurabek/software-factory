package intervention

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

func testService(t *testing.T) (*Service, *store.DB, *task.Service, string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, name := range []string{"planner", "builder", "reviewer"} {
		if err := os.MkdirAll(filepath.Join(root, "prompts", name), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"system.md", "user.md"} {
			if err := os.WriteFile(filepath.Join(root, "prompts", name, file), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	configured := config.Config{
		Defaults: config.Defaults{CodingAgent: "pi", Model: "test/model", Thinking: "low"},
		Agents: []config.Agent{
			{Name: "planner", PromptEngineering: config.PromptEngineering{System: "prompts/planner/system.md", User: "prompts/planner/user.md"}},
			{Name: "builder", PromptEngineering: config.PromptEngineering{System: "prompts/builder/system.md", User: "prompts/builder/user.md"}},
			{Name: "reviewer", PromptEngineering: config.PromptEngineering{System: "prompts/reviewer/system.md", User: "prompts/reviewer/user.md"}},
		},
	}
	configPath := filepath.Join(root, "config.yaml")
	return New(Deps{Store: db, Snapshots: workspace.New(db, nil), Config: configured, ConfigPath: configPath, Root: root}), db, task.New(root, task.Deps{Store: db, Config: configured, ConfigPath: configPath}), root
}

func createTaskWithAttempt(t *testing.T, tasks *task.Service, db *store.DB) (store.Task, store.Phase) {
	t.Helper()
	ctx := context.Background()
	created, err := tasks.Create(ctx, task.CreateRequest{Request: "fix", Repository: task.Repository{Type: "github", Repo: "owner/repository"}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.Task(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase := store.Phase{ID: stagekit.RandomID(), TaskID: created.ID, Sequence: 1, Name: "building", Kind: "agent", Owner: "builder", Description: "build", Status: "failed", Attempt: 1, BranchID: stored.SelectedBranchID, InputSnapshot: "in-snap", OutputSnapshot: "out-snap"}
	if err = db.AddPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if err = db.SetBranchHead(ctx, created.ID, stored.SelectedBranchID, phase.ID); err != nil {
		t.Fatal(err)
	}
	// Seed snapshot rows so materialization is a no-op path.
	_ = db.SaveSnapshot(ctx, store.WorkspaceSnapshot{Digest: "in-snap", TaskID: created.ID, Path: filepath.Join(created.WorkspacePath, "workspace", "repository"), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	_ = db.SaveSnapshot(ctx, store.WorkspaceSnapshot{Digest: "out-snap", TaskID: created.ID, Path: filepath.Join(created.WorkspacePath, "workspace", "repository"), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	return created, phase
}

func TestRetryIsIdempotentAndCreatesOneBranch(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	request := Request{Target: Target{AttemptID: phase.ID}, Intent: "retry", IdempotencyKey: "retry-1"}
	first, err := service.Apply(ctx, created.ID, "tester", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Apply(ctx, created.ID, "tester", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Intervention.ID != second.Intervention.ID || first.AttemptID != second.AttemptID {
		t.Fatalf("idempotency broken: %+v vs %+v", first, second)
	}
	branches, _ := db.Branches(ctx, created.ID)
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2 (initial + child)", len(branches))
	}
	phases, _ := db.Phases(ctx, created.ID)
	if len(phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(phases))
	}
	if phases[1].InputSnapshot != "in-snap" {
		t.Fatalf("retry input snapshot = %q, want in-snap", phases[1].InputSnapshot)
	}
}

func TestStaleBranchHeadHasNoSideEffects(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	beforeBranches, _ := db.Branches(ctx, created.ID)
	beforePhases, _ := db.Phases(ctx, created.ID)
	_, err := service.Apply(ctx, created.ID, "tester", Request{Target: Target{AttemptID: phase.ID}, Intent: "retry", ExpectedBranchHead: "stale-attempt", IdempotencyKey: "stale-1"})
	if err == nil {
		t.Fatal("expected stale_branch error")
	}
	afterBranches, _ := db.Branches(ctx, created.ID)
	afterPhases, _ := db.Phases(ctx, created.ID)
	if len(afterBranches) != len(beforeBranches) || len(afterPhases) != len(beforePhases) {
		t.Fatal("stale head caused side effects")
	}
}

func TestRepairStartsFromOutputSnapshot(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	result, err := service.Apply(ctx, created.ID, "tester", Request{Target: Target{AttemptID: phase.ID}, Intent: "repair", Message: "fix it", IdempotencyKey: "repair-1"})
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := db.PhaseByID(ctx, created.ID, result.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.InputSnapshot != "out-snap" {
		t.Fatalf("repair input snapshot = %q, want out-snap", repaired.InputSnapshot)
	}
	if repaired.Name != "build" || repaired.Kind != "build" || repaired.Owner != "builder" {
		t.Fatalf("repair phase = %+v, want configured build stage", repaired)
	}
}

func TestReviseReopensInPlanningState(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	phase.Name = "plan"
	phase.Kind = "plan"
	phase.Owner = "architect"
	if _, err := db.ExecContext(ctx, `update phases set name=?,kind=?,owner=? where id=?`, phase.Name, phase.Kind, phase.Owner, phase.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update tasks set state='blocked' where id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, created.ID, "tester", Request{Target: Target{AttemptID: phase.ID}, Intent: "revise", Message: "change plan", IdempotencyKey: "revise-1"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Task(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != string(stagekit.Planning) {
		t.Fatalf("revised task state = %q, want planning", reopened.State)
	}
}

func TestStaleAnchorRejected(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	artifactPath := filepath.Join(created.WorkspacePath, "artifact.json")
	if err := os.WriteFile(artifactPath, []byte(`{"title":"current"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateArtifact(ctx, store.Artifact{ID: "art-1", TaskID: created.ID, AttemptID: phase.ID, Type: "plan", Digest: "deadbeef", Path: artifactPath, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	start := 0
	end := 5
	_, err := service.Apply(ctx, created.ID, "tester", Request{Target: Target{ArtifactID: "art-1", Anchor: &Anchor{Kind: "text_range", Start: &start, End: &end, Quote: "Proposed End State Architecture"}}, Intent: "revise", Message: "update", IdempotencyKey: "anchor-1"})
	if err == nil {
		t.Fatal("expected stale_anchor error")
	}
}

func TestInlineArtifactAnchorValidation(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	body := []byte("same text and same text")
	if err := db.CreateArtifact(ctx, store.Artifact{ID: "art-inline", TaskID: created.ID, AttemptID: phase.ID, Type: "plan", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Content: string(body), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	start, end := 0, len("same text")
	if err := service.ValidateAnchor(ctx, created.ID, Target{ArtifactID: "art-inline", Anchor: &Anchor{Kind: "text_range", Start: &start, End: &end, Quote: "same text"}}); err != nil {
		t.Fatalf("valid inline anchor: %v", err)
	}
	start, end = len("same text and "), len(body)
	if err := service.ValidateAnchor(ctx, created.ID, Target{ArtifactID: "art-inline", Anchor: &Anchor{Kind: "text_range", Start: &start, End: &end, Quote: "same text changed"}}); err == nil {
		t.Fatal("expected range quote mismatch to be stale")
	}
}

func TestJSONPointerAnchorValidation(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	body := []byte(`{"items":[{"name":"current"}]}`)
	value := []byte(`"current"`)
	if err := db.CreateArtifact(ctx, store.Artifact{ID: "art-json", TaskID: created.ID, AttemptID: phase.ID, Type: "result", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Content: string(body), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	anchor := &Anchor{Kind: "json_pointer", Pointer: "/items/0/name", ValueHash: fmt.Sprintf("sha256:%x", sha256.Sum256(value))}
	if err := service.ValidateAnchor(ctx, created.ID, Target{ArtifactID: "art-json", Anchor: anchor}); err != nil {
		t.Fatalf("valid JSON pointer: %v", err)
	}
	anchor.ValueHash = "sha256:stale"
	if err := service.ValidateAnchor(ctx, created.ID, Target{ArtifactID: "art-json", Anchor: anchor}); err == nil {
		t.Fatal("expected changed pointer value to be stale")
	}
}

func TestRetryCompletedTaskReopensPreservingHistory(t *testing.T) {
	service, db, tasks, _ := testService(t)
	ctx := context.Background()
	created, phase := createTaskWithAttempt(t, tasks, db)
	if _, err := db.ExecContext(ctx, `update tasks set state='completed',ended_at=? where id=?`, time.Now().UTC().Format(time.RFC3339Nano), created.ID); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, created.ID, "tester", Request{Target: Target{AttemptID: phase.ID}, Intent: "retry", IdempotencyKey: "reopen-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptID == "" || result.BranchID == "" {
		t.Fatal("expected branch and attempt")
	}
	reopened, _ := db.Task(ctx, created.ID)
	if reopened.EndedAt != "" {
		t.Fatalf("ended_at = %q, want cleared", reopened.EndedAt)
	}
	phases, _ := db.Phases(ctx, created.ID)
	if len(phases) != 2 {
		t.Fatalf("phases = %d, want history preserved + new attempt", len(phases))
	}
}

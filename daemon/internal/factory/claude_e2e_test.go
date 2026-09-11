package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	claudeharness "github.com/jurabek/software-factory/daemon/internal/harness/claude"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

const claudeReviewEnvelope = `{"status":"success","summary":"Looks good","artifacts":[],"notes_for_next_agent":"","approved":true,"findings":[],"blocking":[]}`

// fakeClaudeEchoesSession builds a fake claude executable that reads the
// session UUID from argv, consumes stdin, and returns a valid reviewer
// envelope as the terminal result. It exercises argv/stdin/CWD plumbing,
// exact UUID resume, raw-output boundaries, and metadata persistence.
func fakeClaudeEchoesSession(t *testing.T) string {
	t.Helper()
	escaped := strings.ReplaceAll(claudeReviewEnvelope, `"`, `\\\"`)
	script := `#!/bin/sh
SESSION=""
PREV=""
for a in "$@"; do
  if [ "$PREV" = "--session-id" ] || [ "$PREV" = "--resume" ]; then SESSION="$a"; fi
  PREV="$a"
done
if [ -z "$SESSION" ]; then echo "missing session" >&2; exit 2; fi
if [ ! -d "$PWD" ]; then echo "bad cwd" >&2; exit 2; fi
read prompt
echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$SESSION\",\"model\":\"sonnet\"}"
echo "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"$SESSION\",\"result\":\"` + escaped + `\",\"model\":\"sonnet\",\"modelUsage\":{\"sonnet\":{\"inputTokens\":10,\"outputTokens\":5}},\"total_cost_usd\":0.01}"
`
	path := filepath.Join(t.TempDir(), "claude-fake")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeHarnessTaskResumeFlow(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	promptDir := filepath.Join(root, "prompts", "reviewer")
	if err := os.MkdirAll(promptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("Review."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "user.md"), []byte("Review {{.TaskID}}."), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := fakeClaudeEchoesSession(t)
	configRoot := t.TempDir()
	claude := claudeharness.Harness{Config: claudeharness.Config{Path: fake, ConfigRoot: configRoot}}
	cfg := config.Config{
		Defaults: config.Defaults{CodingAgent: "claude", Model: "anthropic/sonnet", Thinking: "medium"},
		Runtime:  config.Runtime{JSONFixAttempts: 1},
		Agents: []config.Agent{{
			Name: "reviewer", Model: "anthropic/sonnet", Thinking: "medium",
			PromptEngineering: config.PromptEngineering{System: "prompts/reviewer/system.md", User: "prompts/reviewer/user.md"},
		}},
	}
	service := NewService(root, Dependencies{
		Store: db, Config: cfg, ConfigPath: filepath.Join(root, "config.yaml"),
		Harnesses: harness.Registry{"claude": claude},
	})
	task, err := service.Create(context.Background(), CreateRequest{Request: "Review change", Repositories: []Repository{{Type: "github", Repo: "owner/repository"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := service.runRole(ctx, task, store.Phase{ID: "phase-1"}, "reviewer", map[string]any{"TaskID": task.ID}, func(text string) (any, error) { return ValidateReview(text) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, `"approved"`) {
		t.Fatalf("first result = %q", first)
	}
	stored, err := db.AgentSession(ctx, task.ID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.SessionReady || stored.HarnessSessionID == "" {
		t.Fatalf("session = %#v, want ready with UUID", stored)
	}
	if stored.Cost != 0.01 {
		t.Fatalf("cost = %v, want 0.01", stored.Cost)
	}
	// Native transcript lands after first initialization; ordinary
	// continuation reuses the exact UUID with Resume=true.
	nativeDir := filepath.Join(configRoot, "projects", "repo")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, stored.HarnessSessionID+".jsonl"), []byte(`{"sessionId":"`+stored.HarnessSessionID+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := service.runRole(ctx, task, store.Phase{ID: "phase-2"}, "reviewer", map[string]any{"TaskID": task.ID}, func(text string) (any, error) { return ValidateReview(text) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, `"approved"`) {
		t.Fatalf("second result = %q", second)
	}
	resumed, err := db.AgentSession(ctx, task.ID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.HarnessSessionID != stored.HarnessSessionID {
		t.Fatalf("resume changed identity %q -> %q", stored.HarnessSessionID, resumed.HarnessSessionID)
	}
	if resumed.Cost != 0.02 {
		t.Fatalf("cost = %v, want 0.02 added once per invocation", resumed.Cost)
	}
	if !resumed.AccountingComplete {
		t.Fatal("accounting_complete = false after two clean invocations, want true")
	}
}

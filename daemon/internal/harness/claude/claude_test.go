package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jurabek/software-factory/daemon/internal/harness"
)

func writeFakeClaude(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-fake")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func successScript(session string) string {
	return `#!/bin/sh
read prompt
echo '{"type":"system","subtype":"init","session_id":"` + session + `","model":"sonnet"}'
echo '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]},"session_id":"` + session + `"}'
echo '{"type":"result","subtype":"success","session_id":"` + session + `","result":"done work","model":"sonnet","modelUsage":{"sonnet":{"inputTokens":10,"outputTokens":5}},"total_cost_usd":0.01}'
`
}

func TestBuildArgsNewVsResume(t *testing.T) {
	h := Harness{Config: Config{AllowedTools: []string{"Read", "Bash"}}}
	id := uuid.NewString()
	newArgs, err := h.BuildArgs(harness.Request{SessionID: id, Model: "anthropic/sonnet", Thinking: "medium", SystemPrompt: "sys", AdditionalDirectories: []string{"/other"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(newArgs, " ")
	for _, want := range []string{"--session-id", id, "--model", "sonnet", "--effort", "medium", "--allowedTools", "Read,Bash", "--add-dir", "/other", "--permission-mode", "dontAsk"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("new args missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "--resume") {
		t.Fatalf("new args must not contain --resume: %q", joined)
	}
	resumeArgs, err := h.BuildArgs(harness.Request{SessionID: id, Model: "sonnet", Thinking: "low", SystemPrompt: "sys"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(resumeArgs, " "), "--resume "+id) {
		t.Fatalf("resume args = %q", resumeArgs)
	}
}

func TestNativeModelAndEffortValidation(t *testing.T) {
	h := Harness{}
	if _, err := h.BuildArgs(harness.Request{SessionID: uuid.NewString(), Model: "github-copilot/gpt-5", Thinking: "medium"}, false); err == nil {
		t.Fatal("expected third-party provider rejection")
	}
	if _, err := h.BuildArgs(harness.Request{SessionID: uuid.NewString(), Model: "sonnet", Thinking: "off"}, false); err == nil {
		t.Fatal("expected off effort rejection")
	}
	if _, err := h.BuildArgs(harness.Request{SessionID: uuid.NewString(), Model: "sonnet", Thinking: "minimal"}, false); err == nil {
		t.Fatal("expected minimal effort rejection")
	}
}

func TestModelsCatalog(t *testing.T) {
	path := writeFakeClaude(t, "#!/bin/sh\nexit 0\n")
	h := Harness{Config: Config{Path: path, Models: []string{"anthropic/sonnet-2026", "github-copilot/gpt"}}}
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]bool{}
	ids := map[string]bool{}
	for _, m := range models {
		providers[m.Provider] = true
		ids[m.ID] = true
		if len(m.Thinking) != 3 || m.Thinking[0] != "low" {
			t.Fatalf("model thinking = %v, want low/medium/high", m.Thinking)
		}
	}
	if len(providers) != 1 || !providers["anthropic"] {
		t.Fatalf("providers = %v, want only anthropic", providers)
	}
	if !ids["sonnet"] || !ids["opus"] || !ids["sonnet-2026"] {
		t.Fatalf("ids = %v", ids)
	}
	if ids["gpt"] {
		t.Fatal("third-party model must be rejected, not mislabelled")
	}
}

func TestRunInitialAndResume(t *testing.T) {
	sessionID := uuid.NewString()
	fake := writeFakeClaude(t, successScript(sessionID))
	root := t.TempDir()
	sessionDir := t.TempDir()
	h := Harness{Config: Config{Path: fake, ConfigRoot: root}}
	request := harness.Request{
		CWD:              t.TempDir(),
		Prompt:           "do work",
		SystemPrompt:     "role instructions",
		Model:            "anthropic/sonnet",
		Thinking:         "medium",
		SessionID:        sessionID,
		SessionDirectory: sessionDir,
		DeadlineMS:       30000,
	}
	result, err := h.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done work" || !result.SessionReady || result.SessionID != sessionID {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage.Cost != 0.01 {
		t.Fatalf("cost = %v", result.Usage.Cost)
	}
	raw, err := os.ReadFile(filepath.Join(sessionDir, "raw-output.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "claude.invocation_boundary") {
		t.Fatal("raw output missing invocation boundary")
	}
	// Native transcript appears after first initialization; second turn
	// resumes the exact UUID.
	nativeDir := filepath.Join(root, "projects", "repo")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, sessionID+".jsonl"), []byte(`{"sessionId":"`+sessionID+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.Resume = true
	request.Prompt = "follow up"
	result2, err := h.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result2.SessionID != sessionID {
		t.Fatalf("resume session = %q, want %q", result2.SessionID, sessionID)
	}
	raw2, _ := os.ReadFile(filepath.Join(sessionDir, "raw-output.jsonl"))
	if strings.Count(string(raw2), "claude.invocation_boundary") != 2 {
		t.Fatal("raw output must record each invocation boundary without mixing accounting")
	}
}

func TestRunMissingNativeStateErrors(t *testing.T) {
	sessionID := uuid.NewString()
	fake := writeFakeClaude(t, successScript(sessionID))
	h := Harness{Config: Config{Path: fake, ConfigRoot: t.TempDir()}}
	request := harness.Request{
		CWD: t.TempDir(), Prompt: "hi", SystemPrompt: "sys",
		Model: "anthropic/sonnet", Thinking: "low",
		SessionID: sessionID, SessionDirectory: t.TempDir(), Resume: true,
	}
	_, err := h.Run(context.Background(), request, nil)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want missing native state", err)
	}
}

func TestRunNonzeroExitWithTextFails(t *testing.T) {
	sessionID := uuid.NewString()
	script := `#!/bin/sh
read prompt
echo '{"type":"system","subtype":"init","session_id":"` + sessionID + `","model":"sonnet"}'
echo '{"type":"result","subtype":"success","session_id":"` + sessionID + `","result":"partial","total_cost_usd":0.01}'
exit 1
`
	fake := writeFakeClaude(t, script)
	h := Harness{Config: Config{Path: fake, ConfigRoot: t.TempDir()}}
	request := harness.Request{
		CWD: t.TempDir(), Prompt: "hi", SystemPrompt: "sys",
		Model: "anthropic/sonnet", Thinking: "low",
		SessionID: sessionID, SessionDirectory: t.TempDir(),
	}
	result, err := h.Run(context.Background(), request, nil)
	if err == nil {
		t.Fatal("expected nonzero exit failure even with text")
	}
	if result.Text != "partial" {
		t.Fatalf("metadata text = %q, want preserved partial", result.Text)
	}
}

func TestRunMalformedStreamFails(t *testing.T) {
	sessionID := uuid.NewString()
	fake := writeFakeClaude(t, "#!/bin/sh\nread prompt\necho 'not json'\n")
	h := Harness{Config: Config{Path: fake, ConfigRoot: t.TempDir()}}
	request := harness.Request{
		CWD: t.TempDir(), Prompt: "hi", SystemPrompt: "sys",
		Model: "anthropic/sonnet", Thinking: "low",
		SessionID: sessionID, SessionDirectory: t.TempDir(),
	}
	_, err := h.Run(context.Background(), request, nil)
	if err == nil {
		t.Fatal("expected malformed stream failure")
	}
}

func TestRunRejectsNonUUID(t *testing.T) {
	h := Harness{Config: Config{Path: "claude", ConfigRoot: t.TempDir()}}
	_, err := h.Run(context.Background(), harness.Request{SessionID: "task-role"}, nil)
	if err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("err = %v, want UUID requirement", err)
	}
}

func TestArchivePathContainment(t *testing.T) {
	nativeDir := t.TempDir()
	sessionID := uuid.NewString()
	nativePath := filepath.Join(nativeDir, sessionID+".jsonl")
	if err := os.WriteFile(nativePath, []byte(`{"sessionId":"`+sessionID+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()
	archived, err := archiveNative(nativePath, sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(archived, sessionDir) {
		t.Fatalf("archive = %q escapes session dir", archived)
	}
}

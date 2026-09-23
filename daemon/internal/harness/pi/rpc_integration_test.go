package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
)

// TestRunnerRPCSessionIntegration exercises the real Pi RPC transport. It is
// skipped unless PI_RPC_INTEGRATION is set and pi is on PATH, so the standard
// repository checks stay hermetic.
func TestRunnerRPCSessionIntegration(t *testing.T) {
	if os.Getenv("PI_RPC_INTEGRATION") == "" {
		t.Skip("set PI_RPC_INTEGRATION=1 to exercise the real pi process")
	}
	path, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi binary not found")
	}
	extension := filepath.Join(t.TempDir(), "factory.ts")
	if err = os.WriteFile(extension, ExtensionSource(), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err = os.WriteFile(filepath.Join(cwd, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := New(path, extension)
	defer runner.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := runner.Open(ctx, harness.SessionSpec{
		SessionID: "11111111-1111-7111-8111-111111111111", SessionDirectory: t.TempDir(),
		CWD: cwd, Model: "openai-codex/gpt-5.5", Thinking: "low", SystemPrompt: "You are terse.",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stats, err := session.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ContextWindow == 0 {
		t.Fatalf("context window = 0, want the model context window: %+v", stats)
	}
}

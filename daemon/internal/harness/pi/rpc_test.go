package pi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/harness"
)

var (
	_ harness.Harness      = (*Runner)(nil)
	_ harness.NativeReader = (*Runner)(nil)
)

func TestStatsFromRPCMapsTokensCostAndContext(t *testing.T) {
	stats := statsFromRPC(json.RawMessage(`{"tokens":{"input":100,"output":40,"cacheRead":10,"cacheWrite":5,"total":155},"cost":0.62,"contextUsage":{"tokens":115,"contextWindow":200000}}`))
	if stats.Usage.Input != 100 || stats.Usage.Output != 40 || stats.Usage.CacheRead != 10 || stats.Usage.CacheWrite != 5 {
		t.Fatalf("usage = %#v", stats.Usage)
	}
	if stats.Usage.TotalTokens != 155 || stats.Usage.Cost != 0.62 {
		t.Fatalf("totals = %#v", stats.Usage)
	}
	if stats.ContextTokens != 115 || stats.ContextWindow != 200000 {
		t.Fatalf("context = %+v", stats)
	}
}

func TestPromptMessageEncodesFactoryRequestWhenExtensionConfigured(t *testing.T) {
	session := &rpcSession{runner: &Runner{ExtensionPath: "/factory.ts"}}
	message, err := session.promptMessage(harness.Prompt{RequestID: "req-1", Attempt: 2, Text: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(message, "/factory-run ") {
		t.Fatalf("message = %q", message)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(message, "/factory-run "))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["requestId"] != "req-1" || payload["prompt"] != "do it" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestPromptMessagePassesThroughWithoutExtension(t *testing.T) {
	session := &rpcSession{runner: &Runner{}}
	message, err := session.promptMessage(harness.Prompt{RequestID: "req-1", Text: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if message != "plain" {
		t.Fatalf("message = %q, want plain prompt", message)
	}
}

func TestSessionMatchesSpec(t *testing.T) {
	session := &rpcSession{spec: harness.SessionSpec{SessionID: "s1", SessionDirectory: "/d", CWD: "/repo", Model: "provider/m", Thinking: "low", SystemPrompt: "sys"}}
	if !session.matches(harness.SessionSpec{SessionID: "s1", SessionDirectory: "/d", CWD: "/repo", Model: "provider/m", Thinking: "low", SystemPrompt: "sys"}) {
		t.Fatal("identical spec did not match")
	}
	if session.matches(harness.SessionSpec{SessionID: "s1", SessionDirectory: "/d", CWD: "/repo", Model: "provider/other", Thinking: "low", SystemPrompt: "sys"}) {
		t.Fatal("differing model matched")
	}
}

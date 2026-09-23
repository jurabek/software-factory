package pi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/harness"
)

func writeSession(t *testing.T, directory, id string, lines []string) {
	t.Helper()
	sub := filepath.Join(directory, "--project--")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, fmt.Sprintf("2026-09-20T00-00-00-000Z_%s.jsonl", id))
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func TestNativeSessionReadsEntriesStatsAndRequestSubtree(t *testing.T) {
	directory := t.TempDir()
	id := "sess-1"
	writeSession(t, directory, id, []string{
		`{"type":"session","version":3,"id":"sess-1","timestamp":"2026-09-20T00:00:00Z","cwd":"/project"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-09-20T00:00:01Z","message":{"role":"user","content":"start"}}`,
		`{"type":"custom","id":"req1","parentId":"u1","timestamp":"2026-09-20T00:00:02Z","customType":"factory-request","data":{"requestId":"req-1","attempt":1}}`,
		`{"type":"message","id":"u2","parentId":"req1","timestamp":"2026-09-20T00:00:03Z","message":{"role":"user","content":"do it"}}`,
		`{"type":"message","id":"a1","parentId":"u2","timestamp":"2026-09-20T00:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"{\"status\":\"success\"}"}],"usage":{"input":100,"output":20,"cacheRead":10,"cacheWrite":5,"totalTokens":120,"cost":{"total":0.5}}}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-09-20T00:00:05Z","message":{"role":"toolResult","content":[{"type":"text","text":"ok"}],"usage":{"input":30,"output":5,"totalTokens":35,"cost":{"total":0.1}}}}`,
	})
	adapter := Harness{}
	ref := harness.SessionRef{ID: id, Directory: directory}

	entries, err := adapter.Entries(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}
	if entries[2].ID != "u2" || entries[2].ParentID != "req1" {
		t.Fatalf("entry[2] = %#v", entries[2])
	}

	stats, err := adapter.Stats(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Usage.Input != 130 || stats.Usage.Output != 25 || stats.Usage.TotalTokens != 155 {
		t.Fatalf("usage = %#v", stats.Usage)
	}
	if stats.Usage.Cost < 0.59 || stats.Usage.Cost > 0.61 {
		t.Fatalf("cost = %v, want 0.6", stats.Usage.Cost)
	}
	if stats.ContextTokens != 115 {
		t.Fatalf("context tokens = %d, want 115", stats.ContextTokens)
	}
	if stats.LeafID != "t1" {
		t.Fatalf("leaf = %q, want t1", stats.LeafID)
	}

	report, ok, err := adapter.Report(context.Background(), ref, "req-1")
	if err != nil || !ok {
		t.Fatalf("report ok=%v err=%v", ok, err)
	}
	if report.Text != `{"status":"success"}` || report.EntryID != "a1" {
		t.Fatalf("report = %#v", report)
	}
	if _, ok, _ := adapter.Report(context.Background(), ref, "missing"); ok {
		t.Fatal("missing request resolved to an assistant turn")
	}
}

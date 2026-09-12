package factory

import (
	"path/filepath"
	"testing"
)

func TestNormalizeRepositoryRequiresAbsoluteLocalPath(t *testing.T) {
	if _, _, err := normalizeRepository(Repository{Type: "local", Path: "relative"}); err == nil {
		t.Fatal("expected absolute-path validation error")
	}
	path := filepath.Join(t.TempDir(), "repository")
	if source, submitted, err := normalizeRepository(Repository{Type: "local", Path: path}); err != nil || source != path || submitted != path {
		t.Fatalf("normalizeRepository() = %q, %q, %v", source, submitted, err)
	}
}

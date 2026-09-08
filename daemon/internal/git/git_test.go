package git

import (
	"context"
	"testing"
)

type emptyRunner struct{}

func (emptyRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestChangedFilesReturnsEmptySliceWhenRepositoryIsUnchanged(t *testing.T) {
	files, err := ChangedFiles(context.Background(), emptyRunner{}, "/repository")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if files == nil {
		t.Fatal("ChangedFiles() returned nil; want an empty slice for JSON arrays")
	}
	if len(files) != 0 {
		t.Fatalf("len(ChangedFiles()) = %d, want 0", len(files))
	}
}

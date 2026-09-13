package factory

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Service) CaptureSnapshot(ctx context.Context, task store.Task) (store.WorkspaceSnapshot, error) {
	return s.snapshots.CaptureSnapshot(ctx, task)
}

func (s *Service) MaterializeSnapshot(ctx context.Context, task store.Task, digest string) error {
	return s.snapshots.MaterializeSnapshot(ctx, task, digest)
}

func (s *Service) MaterializeScratch(ctx context.Context, task store.Task, digest, destination string) error {
	return s.snapshots.MaterializeScratch(ctx, task, digest, destination)
}

func withinPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

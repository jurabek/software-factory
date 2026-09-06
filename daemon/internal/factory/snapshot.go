package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jurabek/software-factory/internal/store"
)

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type fileEntry struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

// CaptureSnapshot copies workspace/repositories into workspace/snapshots/<digest>/.
func (s *Service) CaptureSnapshot(ctx context.Context, task store.Task) (store.WorkspaceSnapshot, error) {
	source := filepath.Join(task.WorkspacePath, "workspace", "repositories")
	destinationRoot := filepath.Join(task.WorkspacePath, "workspace", "snapshots")
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		return store.WorkspaceSnapshot{}, err
	}
	entries := make([]fileEntry, 0)
	hasher := sha256.New()
	if _, err := os.Stat(source); err == nil {
		err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			fileHash := ""
			if info.Mode().IsRegular() {
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				hash := sha256.New()
				if _, err = io.Copy(hash, file); err != nil {
					file.Close()
					return err
				}
				file.Close()
				fileHash = hex.EncodeToString(hash.Sum(nil))
			}
			entries = append(entries, fileEntry{Path: filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm()), Size: info.Size(), Hash: fileHash})
			fmt.Fprintf(hasher, "%s\x00%d\x00%d\x00%s\x00", filepath.ToSlash(relative), info.Mode().Perm(), info.Size(), fileHash)
			return nil
		})
		if err != nil {
			return store.WorkspaceSnapshot{}, fmt.Errorf("hash workspace: %w", err)
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest == "" {
		empty := sha256.Sum256(nil)
		digest = hex.EncodeToString(empty[:])
	}
	destination := filepath.Join(destinationRoot, digest)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return store.WorkspaceSnapshot{}, err
	}
	if _, err := os.Stat(source); err == nil {
		if err := copyDir(source, destination); err != nil {
			return store.WorkspaceSnapshot{}, err
		}
	}
	manifest, _ := json.Marshal(entries)
	var size int64
	for _, entry := range entries {
		size += entry.Size
	}
	snapshot := store.WorkspaceSnapshot{Digest: digest, TaskID: task.ID, Path: destination, SizeBytes: size, Manifest: string(manifest), CreatedAt: nowString()}
	if err := s.db.SaveSnapshot(ctx, snapshot); err != nil {
		return store.WorkspaceSnapshot{}, err
	}
	return snapshot, nil
}

// MaterializeSnapshot restores a snapshot into workspace/repositories.
func (s *Service) MaterializeSnapshot(ctx context.Context, task store.Task, digest string) error {
	if digest == "" {
		return nil
	}
	snapshot, err := s.db.Snapshot(ctx, digest)
	if err != nil {
		return err
	}
	destination := filepath.Join(task.WorkspacePath, "workspace", "repositories")
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(snapshot.Path); os.IsNotExist(err) {
		return nil
	}
	return copyDir(snapshot.Path, destination)
}

func copyDir(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
}

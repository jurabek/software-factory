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
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type fileEntry struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

// CaptureSnapshot copies workspace/repositories into workspace/snapshots/<digest>/.
func (s *snapshotService) CaptureSnapshot(ctx context.Context, task store.Task) (store.WorkspaceSnapshot, error) {
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
				relative, relativeErr := filepath.Rel(source, path)
				if relativeErr == nil && (relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator))) {
					return filepath.SkipDir
				}
				return nil
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
				return nil
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
func (s *snapshotService) MaterializeSnapshot(ctx context.Context, task store.Task, digest string) error {
	if digest == "" {
		return nil
	}
	snapshot, err := s.db.Snapshot(ctx, digest)
	if err != nil {
		return err
	}
	destination := filepath.Join(task.WorkspacePath, "workspace", "repositories")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(snapshot.Path); os.IsNotExist(err) {
		return nil
	}
	if err := clearRepositoryContents(destination); err != nil {
		return err
	}
	return copyDir(snapshot.Path, destination)
}

func (s *snapshotService) MaterializeScratch(ctx context.Context, task store.Task, digest, destination string) error {
	if digest == "" {
		return fmt.Errorf("comparison snapshot is required")
	}
	snapshot, err := s.db.Snapshot(ctx, digest)
	if err != nil {
		return err
	}
	if snapshot.TaskID != task.ID {
		return fmt.Errorf("comparison snapshot belongs to another task")
	}
	if err = os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	if err = copyDirSafe(snapshot.Path, destination); err != nil {
		return fmt.Errorf("materialize comparison snapshot: %w", err)
	}
	if s.git == nil {
		return fmt.Errorf("git runner is required")
	}
	for _, repository := range task.Repositories {
		repositoryPath := filepath.Join(destination, repository.Name)
		if err = os.MkdirAll(repositoryPath, 0o700); err != nil {
			return err
		}
		if _, err = s.git.Run(ctx, "git", "-C", repositoryPath, "init"); err != nil {
			return fmt.Errorf("initialize scratch repository %s: %w", repository.Name, err)
		}
		if _, err = s.git.Run(ctx, "git", "-C", repositoryPath, "add", "--all"); err != nil {
			return fmt.Errorf("stage scratch repository %s: %w", repository.Name, err)
		}
		if _, err = s.git.Run(ctx, "git", "-C", repositoryPath, "-c", "user.name=Software Factory", "-c", "user.email=software-factory@localhost", "commit", "--allow-empty", "-m", "comparison snapshot"); err != nil {
			return fmt.Errorf("commit scratch repository %s: %w", repository.Name, err)
		}
	}
	return nil
}

func (s *Service) CaptureSnapshot(ctx context.Context, task store.Task) (store.WorkspaceSnapshot, error) {
	return s.snapshots.CaptureSnapshot(ctx, task)
}

func (s *Service) MaterializeSnapshot(ctx context.Context, task store.Task, digest string) error {
	return s.snapshots.MaterializeSnapshot(ctx, task, digest)
}

func (s *Service) MaterializeScratch(ctx context.Context, task store.Task, digest, destination string) error {
	return s.snapshots.MaterializeScratch(ctx, task, digest, destination)
}

func clearRepositoryContents(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.Name() == ".git" {
				continue
			}
			if err := os.RemoveAll(filepath.Join(path, child.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyDir(source, destination string) error {
	return copyDirChecked(source, destination, false)
}

func copyDirSafe(source, destination string) error {
	return copyDirChecked(source, destination, true)
}

func copyDirChecked(source, destination string, rejectEscapingSymlinks bool) error {
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
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
		if rejectEscapingSymlinks && isGitMetadataPath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
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
			if rejectEscapingSymlinks {
				if filepath.IsAbs(link) {
					return fmt.Errorf("symlink escapes snapshot: %s", relative)
				}
				resolvedLink, resolveErr := filepath.EvalSymlinks(path)
				if resolveErr != nil || !withinPath(resolvedSource, resolvedLink) {
					return fmt.Errorf("symlink escapes snapshot: %s", relative)
				}
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if err = os.Chmod(target, info.Mode().Perm()); err != nil {
			return err
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
}

func isGitMetadataPath(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".git" {
			return true
		}
	}
	return false
}

func withinPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

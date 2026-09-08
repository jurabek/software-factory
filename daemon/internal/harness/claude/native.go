package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// configRoot resolves the Claude native state root: explicit override,
// CLAUDE_CONFIG_DIR, or ~/.claude.
func configRoot(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude config root: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// findNativeTranscript locates the exact reserved UUID under the native state
// root without scanning unrelated histories. It matches only filename metadata
// (<UUID>.jsonl) under projects/*, then verifies the file carries the same
// session identity.
func findNativeTranscript(root, uuid string) (path string, ok bool, err error) {
	if uuid == "" {
		return "", false, nil
	}
	projects := filepath.Join(root, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read claude projects: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projects, entry.Name(), uuid+".jsonl")
		info, statErr := os.Stat(candidate)
		if statErr != nil || info.IsDir() {
			continue
		}
		// Verify identity from file metadata (first record session id).
		match, verifyErr := verifyTranscriptIdentity(candidate, uuid)
		if verifyErr != nil {
			return "", false, verifyErr
		}
		if match {
			return candidate, true, nil
		}
		// Filename collision with different identity is ambiguity: stop.
		return "", false, fmt.Errorf("claude native transcript %s has mismatched identity", candidate)
	}
	return "", false, nil
}

func verifyTranscriptIdentity(path, uuid string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("read claude transcript: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(file, 256*1024)
	decoder := json.NewDecoder(limited)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				return false, nil
			}
			// Unreadable content is ambiguity, not absence.
			return false, fmt.Errorf("decode claude transcript: %w", err)
		}
		for _, key := range []string{"sessionId", "session_id"} {
			if value, _ := record[key].(string); value != "" {
				return value == uuid, nil
			}
		}
		if message, _ := record["message"].(map[string]any); message != nil {
			continue
		}
	}
}

// archiveNative copies the identified native session and its associated
// subagent/tool-result files beneath the task-owned session directory.
// Repeat archives replace snapshots. Paths must resolve beneath verified
// session roots; traversal/symlink escapes are rejected.
func archiveNative(nativePath, sessionDir, uuid string) (string, error) {
	if nativePath == "" {
		return "", nil
	}
	allowedRoot := filepath.Dir(nativePath)
	resolved, err := filepath.EvalSymlinks(nativePath)
	if err != nil {
		return "", fmt.Errorf("resolve claude transcript: %w", err)
	}
	if !strings.HasPrefix(resolved, allowedRoot) && resolved != nativePath {
		return "", fmt.Errorf("claude transcript escapes session root")
	}
	destinationDir := filepath.Join(sessionDir, "native")
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return "", fmt.Errorf("create claude archive: %w", err)
	}
	destination := filepath.Join(destinationDir, uuid+".jsonl")
	if err := copyFile(nativePath, destination); err != nil {
		return "", err
	}
	// Associated files share the UUID prefix in the same native directory.
	entries, err := os.ReadDir(allowedRoot)
	if err != nil {
		return destination, nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), uuid+"-") {
			continue
		}
		source := filepath.Join(allowedRoot, entry.Name())
		if resolvedSibling, err := filepath.EvalSymlinks(source); err != nil || !strings.HasPrefix(resolvedSibling, allowedRoot) {
			continue
		}
		_ = copyFile(source, filepath.Join(destinationDir, entry.Name()))
	}
	return destination, nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("read claude transcript: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write claude archive: %w", err)
	}
	defer output.Close()
	if _, err := io.Copy(output, io.LimitReader(input, 64<<20)); err != nil {
		return fmt.Errorf("copy claude transcript: %w", err)
	}
	return output.Sync()
}

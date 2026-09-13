package verifier

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxCapturedOutput = 64 << 10

func randomID() string {
	var bytes [12]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func withinPath(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type tailCapture struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (capture *tailCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	length := len(data)
	capture.data = append(capture.data, data...)
	if len(capture.data) > capture.limit {
		capture.data = capture.data[len(capture.data)-capture.limit:]
	}
	return length, nil
}

func (capture *tailCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return string(capture.data)
}

package factory

import (
	"crypto/sha256"
	"fmt"
	"time"
)

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func planDigest(key string, revision int, amendment string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", key, revision, amendment)))
	return fmt.Sprintf("%x", sum)
}

package store

type WorkspaceSnapshot struct {
	Digest    string `json:"digest"`
	TaskID    string `json:"task_id"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Manifest  string `json:"manifest_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

package stage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Common is the envelope shape every agent-authored stage payload shares.
type Common struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts"`
	Notes     string   `json:"notes_for_next_agent"`
	Report    string   `json:"report_markdown"`
}

// CommonFields lists the shared envelope fields accepted by every stage.
var CommonFields = []string{"status", "summary", "artifacts", "notes_for_next_agent", "report_markdown"}

// DecodeExact decodes a JSON envelope, rejecting unknown and missing fields.
func DecodeExact(text string, target any, required, allowed []string) error {
	body, err := object(text)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(body, &raw); err != nil {
		return err
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range raw {
		if !allowedSet[key] {
			return fmt.Errorf("unknown envelope field %q", key)
		}
	}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			return fmt.Errorf("missing envelope field %q", key)
		}
	}
	return nil
}

// ValidateCommon enforces the invariants shared by all stage envelopes.
func ValidateCommon(value Common) error {
	if value.Status != "success" {
		return fmt.Errorf("envelope status must be success")
	}
	if strings.TrimSpace(value.Summary) == "" {
		return fmt.Errorf("envelope summary is required")
	}
	if value.Artifacts == nil {
		return fmt.Errorf("envelope artifacts array is required")
	}
	if strings.TrimSpace(value.Report) == "" {
		return fmt.Errorf("envelope report_markdown is required")
	}
	if len(value.Report) > 256<<10 {
		return fmt.Errorf("envelope report_markdown exceeds 256 KiB")
	}
	return nil
}

// CommonInstructions renders the shared field example used in prompts.
func CommonInstructions() string {
	return `"status":"success","summary":"...","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Report\n\n..."`
}

func object(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		first := strings.Index(trimmed, "\n")
		last := strings.LastIndex(trimmed, "```")
		if first >= 0 && last > first {
			trimmed = strings.TrimSpace(trimmed[first+1 : last])
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("envelope is not a JSON object")
	}
	return []byte(trimmed[start : end+1]), nil
}

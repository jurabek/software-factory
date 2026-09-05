package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type Common struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts"`
	Notes     string   `json:"notes_for_next_agent"`
}
type PlanStep struct {
	ID                 string   `json:"id"`
	Description        string   `json:"description"`
	ExpectedFiles      []string `json:"expected_files"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}
type Plan struct {
	Common
	Steps    []PlanStep `json:"steps"`
	Questions []string  `json:"questions"`
}
type Build struct {
	Common
	ChangedFiles  []string `json:"changed_files"`
	CommitMessage string   `json:"commit_message"`
}
type Finding struct {
	Requirement string `json:"requirement"`
	Met         bool   `json:"met"`
	Evidence    string `json:"evidence"`
}
type Review struct {
	Common
	Approved bool      `json:"approved"`
	Findings []Finding `json:"findings"`
	Blocking []string  `json:"blocking"`
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

func decodeExact(text string, target any, required, allowed []string) (map[string]json.RawMessage, error) {
	body, err := object(text)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range raw {
		if !allowedSet[key] {
			return nil, fmt.Errorf("unknown envelope field %q", key)
		}
	}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			return nil, fmt.Errorf("missing envelope field %q", key)
		}
	}
	return raw, nil
}

var commonFields = []string{"status", "summary", "artifacts", "notes_for_next_agent"}

func validateCommon(value Common) error {
	if value.Status != "success" {
		return fmt.Errorf("envelope status must be success")
	}
	if strings.TrimSpace(value.Summary) == "" {
		return fmt.Errorf("envelope summary is required")
	}
	if value.Artifacts == nil {
		return fmt.Errorf("envelope artifacts array is required")
	}
	return nil
}

func ValidatePlan(text string) (Plan, error) {
	var value Plan
	fields := append(append([]string{}, commonFields...), "steps", "questions")
	if _, err := decodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := validateCommon(value.Common); err != nil {
		return value, err
	}
	if len(value.Steps) == 0 {
		return value, fmt.Errorf("planner steps are required")
	}
	if value.Questions == nil {
		return value, fmt.Errorf("planner questions array is required")
	}
	for _, question := range value.Questions {
		if strings.TrimSpace(question) == "" {
			return value, fmt.Errorf("planner questions cannot contain blank entries")
		}
	}
	seen := map[string]bool{}
	for _, step := range value.Steps {
		if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Description) == "" || step.ExpectedFiles == nil || step.AcceptanceCriteria == nil || seen[step.ID] {
			return value, fmt.Errorf("invalid or duplicate plan step")
		}
		seen[step.ID] = true
	}
	return value, nil
}

func ValidateBuild(text string) (Build, error) {
	var value Build
	fields := append(append([]string{}, commonFields...), "changed_files", "commit_message")
	if _, err := decodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := validateCommon(value.Common); err != nil {
		return value, err
	}
	if value.ChangedFiles == nil {
		return value, fmt.Errorf("builder changed_files array is required")
	}
	return value, nil
}

func ValidateReview(text string) (Review, error) {
	var value Review
	fields := append(append([]string{}, commonFields...), "approved", "findings", "blocking")
	if _, err := decodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := validateCommon(value.Common); err != nil {
		return value, err
	}
	if value.Findings == nil || value.Blocking == nil {
		return value, fmt.Errorf("review findings and blocking arrays are required")
	}
	for _, finding := range value.Findings {
		if strings.TrimSpace(finding.Requirement) == "" || strings.TrimSpace(finding.Evidence) == "" {
			return value, fmt.Errorf("review finding requires requirement and evidence")
		}
		if !finding.Met && value.Approved {
			return value, fmt.Errorf("approved review has unmet finding")
		}
	}
	if value.Approved && len(value.Blocking) > 0 {
		return value, fmt.Errorf("approved review has blocking findings")
	}
	if !value.Approved && len(value.Blocking) == 0 {
		return value, fmt.Errorf("rejected review requires blocking findings")
	}
	return value, nil
}

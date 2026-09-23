package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/harness"
)

// factoryRequestType is the custom entry type the factory extension appends
// immediately before sending the user message for a factory request.
const factoryRequestType = "factory-request"

// factoryRequestGrant maps a factory request id to its native subtree root.
type factoryRequestGrant struct {
	entryID   string
	requestID string
}

type sessionRecord struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	ParentID   *string         `json:"parentId"`
	Timestamp  string          `json:"timestamp"`
	CustomType string          `json:"customType"`
	Data       json.RawMessage `json:"data"`
	Message    *sessionMessage `json:"message"`
	Usage      *rawUsage       `json:"usage"`
}

type sessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   *rawUsage       `json:"usage"`
}

type rawUsage struct {
	Input       int     `json:"input"`
	Output      int     `json:"output"`
	CacheRead   int     `json:"cacheRead"`
	CacheWrite  int     `json:"cacheWrite"`
	Reasoning   int     `json:"reasoning"`
	TotalTokens int     `json:"totalTokens"`
	Cost        rawCost `json:"cost"`
}

type rawCost struct {
	Total float64 `json:"total"`
}

// Entries reads the full native session tree, including abandoned branches and
// pre-compaction history. It is the authoritative record, not get_messages.
func (h Harness) Entries(_ context.Context, ref harness.SessionRef) ([]harness.NativeEntry, error) {
	records, err := readSession(ref.Directory, ref.ID)
	if err != nil {
		return nil, err
	}
	return nativeEntries(records), nil
}

// nativeEntries projects native session records into the harness entry shape.
func nativeEntries(records []sessionRecord) []harness.NativeEntry {
	entries := make([]harness.NativeEntry, 0, len(records))
	for _, record := range records {
		if record.Type == "session" || record.ID == "" {
			continue
		}
		parent := ""
		if record.ParentID != nil {
			parent = *record.ParentID
		}
		entry := harness.NativeEntry{ID: record.ID, ParentID: parent, Type: record.Type}
		if record.Message != nil {
			entry.Role = record.Message.Role
			entry.Text = messageText(record.Message.Content)
			entry.Data, _ = json.Marshal(record.Message)
		} else {
			entry.Data = record.Data
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp); parseErr == nil {
			entry.Timestamp = parsed
		}
		entries = append(entries, entry)
	}
	return entries
}

// Stats derives usage and cost totals from the native session, including tool
// work and compaction, matching Pi's own session totals.
func (h Harness) Stats(_ context.Context, ref harness.SessionRef) (harness.Stats, error) {
	records, err := readSession(ref.Directory, ref.ID)
	if err != nil {
		return harness.Stats{}, err
	}
	var stats harness.Stats
	for _, record := range records {
		if record.ID != "" && record.Type != "session" {
			stats.LeafID = record.ID
		}
		usage := record.Usage
		if record.Message != nil && record.Message.Usage != nil {
			usage = record.Message.Usage
		}
		if usage == nil {
			continue
		}
		stats.Usage.Input += usage.Input
		stats.Usage.Output += usage.Output
		stats.Usage.CacheRead += usage.CacheRead
		stats.Usage.CacheWrite += usage.CacheWrite
		stats.Usage.Reasoning += usage.Reasoning
		stats.Usage.TotalTokens += usage.TotalTokens
		stats.Usage.Cost += usage.Cost.Total
		if record.Message != nil && record.Message.Role == "assistant" {
			stats.ContextTokens = usage.Input + usage.CacheRead + usage.CacheWrite
		}
	}
	return stats, nil
}

// Report resolves the assistant text produced by a factory request. It uses
// the recorded factory-request subtree rather than the active leaf, so a
// correction branch still resolves to its own final response. The returned
// report carries the exact native entry that authored it.
func (h Harness) Report(_ context.Context, ref harness.SessionRef, requestID string) (harness.Report, bool, error) {
	if requestID == "" {
		return harness.Report{}, false, nil
	}
	records, err := readSession(ref.Directory, ref.ID)
	if err != nil {
		return harness.Report{}, false, nil
	}
	report, ok := reportFromRecords(records, requestID)
	return report, ok, nil
}

func reportFromRecords(records []sessionRecord, requestID string) (harness.Report, bool) {
	grant, ok := findRequest(records, requestID)
	if !ok {
		return harness.Report{}, false
	}
	descendants := subtree(records, grant.entryID)
	report := harness.Report{}
	for _, record := range records {
		if !descendants[record.ID] || record.Message == nil || !strings.EqualFold(record.Message.Role, "assistant") {
			continue
		}
		if text := messageText(record.Message.Content); strings.TrimSpace(text) != "" {
			report = harness.Report{EntryID: record.ID, Text: text}
		}
	}
	if report.Text == "" {
		return harness.Report{}, false
	}
	return report, true
}

func findRequest(records []sessionRecord, requestID string) (factoryRequestGrant, bool) {
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.Type != "custom" || record.CustomType != factoryRequestType {
			continue
		}
		var data struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(record.Data, &data) == nil && data.RequestID == requestID {
			return factoryRequestGrant{entryID: record.ID, requestID: requestID}, true
		}
	}
	return factoryRequestGrant{}, false
}

func subtree(records []sessionRecord, rootID string) map[string]bool {
	children := map[string][]string{}
	for _, record := range records {
		if record.ParentID != nil {
			children[*record.ParentID] = append(children[*record.ParentID], record.ID)
		}
	}
	seen := map[string]bool{}
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, children[id]...)
	}
	return seen
}

func messageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var builder strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return builder.String()
	}
	return ""
}

// readSession loads every record in the native session file for a session id,
// in append order. A missing file surfaces as an error so callers can fall
// back to the streamed result instead of failing the turn.
func readSession(directory, id string) ([]sessionRecord, error) {
	path, err := sessionFile(directory, id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	records := make([]sessionRecord, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record sessionRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func sessionFile(directory, id string) (string, error) {
	if directory == "" {
		return "", errors.New("pi session directory is empty")
	}
	var matches []string
	_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		base := entry.Name()
		if id == "" || strings.HasSuffix(base, "_"+id+".jsonl") || base == id+".jsonl" {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil {
			return matches[i] < matches[j]
		}
		return left.ModTime().Before(right.ModTime())
	})
	return matches[len(matches)-1], nil
}

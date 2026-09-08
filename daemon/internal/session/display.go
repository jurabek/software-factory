package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

var toolTitles = map[string]string{
	"applypatch": "Edit",
	"bash":       "Bash",
	"edit":       "Edit",
	"glob":       "Files",
	"grep":       "Search",
	"read":       "Read",
	"webfetch":   "Web Fetch",
	"websearch":  "Web Search",
	"write":      "Write",
	"agent":      "Agent",
}

func toolTitle(name string) string {
	key := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
	if title, ok := toolTitles[key]; ok {
		return title
	}
	return displayName(name)
}

func displayName(value string) string {
	value = camelBoundary.ReplaceAllString(value, "$1 $2")
	value = strings.NewReplacer("_", " ", "-", " ").Replace(value)
	words := strings.Fields(value)
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func toolTarget(arguments json.RawMessage) string {
	var values map[string]any
	if json.Unmarshal(arguments, &values) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "command", "pattern", "query", "url"} {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncate(value, maxTargetBytes)
		}
	}
	return ""
}

func firstLine(value string, limit int) string {
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncate(line, limit)
		}
	}
	return ""
}

// Truncate keeps the beginning of a string and never splits a UTF-8 sequence.
// The returned string is at most limit bytes, including its marker.
func Truncate(value string, limit int) string {
	return truncate(value, limit)
}

func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	marker := "..."
	if limit <= len(marker) {
		return string([]rune(value)[:0]) + marker[:limit]
	}
	keep := limit - len(marker)
	for keep > 0 && !utf8.ValidString(value[:keep]) {
		keep--
	}
	return value[:keep] + marker
}

// BoundedJSON returns valid JSON no larger than the supplied limit, or
// MaxJSONBytes when no limit is supplied. Values that do not fit are
// represented by a valid object rather than a sliced JSON prefix.
func BoundedJSON(value any, limits ...int) json.RawMessage {
	limit := MaxJSONBytes
	if len(limits) > 0 {
		limit = limits[0]
	}
	return BoundedJSONLimit(value, limit)
}

// BoundedJSONLimit is the configurable form of BoundedJSON, useful for
// callers enforcing a smaller transport limit.
func BoundedJSONLimit(value any, limit int) json.RawMessage {
	if limit <= 0 {
		return json.RawMessage("null")
	}
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) <= limit && json.Valid(encoded) {
		return json.RawMessage(encoded)
	}

	preview := ""
	if err == nil {
		preview = string(encoded)
	} else {
		preview = fmt.Sprint(value)
	}
	for {
		candidate, marshalErr := json.Marshal(struct {
			Truncated bool   `json:"truncated"`
			Preview   string `json:"preview"`
		}{true, preview})
		if marshalErr == nil && len(candidate) <= limit {
			return json.RawMessage(candidate)
		}
		if preview == "" {
			break
		}
		preview = truncate(preview, len(preview)/2)
	}
	// JSON has a four-byte minimum representation. Normal contract limits are
	// much larger; retain valid JSON for defensive callers with tiny limits.
	if limit >= 2 {
		return json.RawMessage("{}")
	}
	return json.RawMessage("null")
}

// ValidJSON reports whether data is valid JSON and within the contract bound.
func ValidJSON(data json.RawMessage) bool {
	return len(data) <= MaxJSONBytes && json.Valid(bytes.TrimSpace(data))
}

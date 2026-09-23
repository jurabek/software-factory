package stagekit

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// RenderPrompts resolves system/user templates from inline content or config
// directory paths and appends stage-owned instructions to the system prompt.
// Prompt history is owned by the native harness session, so no factory audit
// copy is written. Template data stays owned by the calling stage; only the
// mechanics are shared.
func RenderPrompts(agentName, systemContent, systemPath, userContent, userPath string, data map[string]any, configBaseDir, instructions string) (string, string, error) {
	system, err := renderTemplate(systemContent, systemPath, configBaseDir, filepath.Base(systemPath), data)
	if err != nil {
		return "", "", err
	}
	system = strings.TrimSpace(system) + "\n\n" + instructions
	user, err := renderTemplate(userContent, userPath, configBaseDir, filepath.Base(userPath), data)
	if err != nil {
		return "", "", err
	}
	return system, user, nil
}

func renderTemplate(content, path, baseDir, name string, data map[string]any) (string, error) {
	if content != "" {
		return executeTemplate("frozen-prompt", content, data)
	}
	body, err := os.ReadFile(filepath.Join(baseDir, path))
	if err != nil {
		return "", err
	}
	return executeTemplate(name, string(body), data)
}

func executeTemplate(name, body string, data map[string]any) (string, error) {
	parsed, err := template.New(name).Option("missingkey=zero").Parse(body)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	if err = parsed.Execute(&output, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

package agentexec

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// RenderPrompts resolves system/user templates from inline content or config
// directory paths, appends stage-owned instructions to the system prompt, and
// audits both prompts under auditDir. Template data stays owned by the
// calling stage; only the mechanics are shared.
func RenderPrompts(agentName, systemContent, systemPath, userContent, userPath string, data map[string]any, configBaseDir, auditDir, instructions string) (string, string, error) {
	system, err := renderTemplate(systemContent, systemPath, configBaseDir, filepath.Base(systemPath), data)
	if err != nil {
		return "", "", err
	}
	system = strings.TrimSpace(system) + "\n\n" + instructions
	user, err := renderTemplate(userContent, userPath, configBaseDir, filepath.Base(userPath), data)
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(auditDir, 0o700); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(auditDir, "system.md"), []byte(system), 0o600); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(auditDir, "user.md"), []byte(user), 0o600); err != nil {
		return "", "", err
	}
	return system, user, nil
}

func renderTemplate(content, path, baseDir, name string, data map[string]any) (string, error) {
	if content != "" {
		parsed, err := template.New("frozen-prompt").Option("missingkey=zero").Parse(content)
		if err != nil {
			return "", err
		}
		var output strings.Builder
		if err = parsed.Execute(&output, data); err != nil {
			return "", err
		}
		return output.String(), nil
	}
	body, err := os.ReadFile(filepath.Join(baseDir, path))
	if err != nil {
		return "", err
	}
	parsed, err := template.New(name).Option("missingkey=zero").Parse(string(body))
	if err != nil {
		return "", err
	}
	var output strings.Builder
	if err = parsed.Execute(&output, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

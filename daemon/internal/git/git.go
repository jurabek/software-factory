package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Check struct {
	ID      string `json:"id" yaml:"id"`
	Command string `json:"command" yaml:"command"`
}
type Profile struct {
	Root         string   `json:"root"`
	SourceType   string   `json:"source_type"`
	Source       string   `json:"source"`
	BaseSHA      string   `json:"base_sha"`
	Checks       []Check  `json:"checks"`
	Generated    []string `json:"generated"`
	Protected    []string `json:"protected"`
	Instructions []string `json:"instructions"`
}

type directives struct {
	Checks    []Check  `yaml:"checks"`
	Generated []string `yaml:"generated"`
	Protected []string `yaml:"protected"`
}

func ResolveRoot(ctx context.Context, runner Runner, path string) (string, string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("repository path is not a directory")
	}
	rootOutput, err := runner.Run(ctx, "git", "-C", resolved, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("find git root: %w", err)
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return "", "", fmt.Errorf("canonical git root: %w", err)
	}
	head, err := runner.Run(ctx, "git", "-C", root, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("read git head: %w", err)
	}
	return root, strings.TrimSpace(string(head)), nil
}

func PrepareLocal(ctx context.Context, runner Runner, source, destination string) (Profile, error) {
	root, sha, err := ResolveRoot(ctx, runner, source)
	if err != nil {
		return Profile{}, err
	}
	if output, runErr := runner.Run(ctx, "git", "-C", root, "worktree", "add", "--detach", destination, sha); runErr != nil {
		return Profile{}, fmt.Errorf("create worktree: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	profile, err := profile(destination, "local", source, sha)
	if err != nil {
		return Profile{}, err
	}
	profile.Root = root
	return profile, nil
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func PrepareGitHub(ctx context.Context, runner Runner, repository, destination string) (Profile, error) {
	if !githubRepository.MatchString(repository) {
		return Profile{}, fmt.Errorf("github repository must be owner/repository")
	}
	if output, err := runner.Run(ctx, "gh", "repo", "clone", repository, destination); err != nil {
		return Profile{}, fmt.Errorf("clone github repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	head, err := runner.Run(ctx, "git", "-C", destination, "rev-parse", "HEAD")
	if err != nil {
		return Profile{}, fmt.Errorf("read cloned head: %w", err)
	}
	return profile(destination, "github", repository, strings.TrimSpace(string(head)))
}

func profile(workspace, sourceType, source, sha string) (Profile, error) {
	checks, generated, protected, err := DetectProfile(workspace)
	if err != nil {
		return Profile{}, err
	}
	instructions := findAgentInstructions(workspace)
	return Profile{Root: workspace, SourceType: sourceType, Source: source, BaseSHA: sha, Checks: checks, Generated: generated, Protected: protected, Instructions: instructions}, nil
}

func ChecksFromAgents(path string) ([]Check, bool, error) {
	checks, _, _, found, err := directivesFromAgents(path)
	return checks, found, err
}

func directivesFromAgents(path string) ([]Check, []string, []string, bool, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, nil, false, err
	}
	text := string(body)
	start := strings.Index(text, "<!-- software-factory:start -->")
	end := strings.Index(text, "<!-- software-factory:end -->")
	if start < 0 || end <= start {
		return nil, nil, nil, false, nil
	}
	block := strings.TrimSpace(text[start+len("<!-- software-factory:start -->") : end])
	lines := strings.Split(block, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[:len(lines)-1]
	}
	var value directives
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &value); err != nil {
		return nil, nil, nil, true, fmt.Errorf("parse AGENTS.md factory block: %w", err)
	}
	seen := map[string]bool{}
	for _, check := range value.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Command) == "" || seen[check.ID] {
			return nil, nil, nil, true, fmt.Errorf("checks require unique non-empty IDs and commands")
		}
		seen[check.ID] = true
	}
	for _, path := range append(append([]string{}, value.Generated...), value.Protected...) {
		if err := validateRelativePath(path); err != nil {
			return nil, nil, nil, true, err
		}
	}
	return value.Checks, value.Generated, value.Protected, true, nil
}

func DetectProfile(root string) ([]Check, []string, []string, error) {
	if checks, generated, protected, found, err := directivesFromAgents(filepath.Join(root, "AGENTS.md")); found || err != nil {
		return checks, generated, protected, err
	}
	checks, err := detectChecks(root)
	return checks, nil, nil, err
}

func DetectChecks(root string) ([]Check, error) {
	checks, _, _, err := DetectProfile(root)
	return checks, err
}

func detectChecks(root string) ([]Check, error) {
	var checks []Check
	if body, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(body, &pkg) == nil {
			for _, name := range []string{"test", "typecheck", "lint"} {
				if strings.TrimSpace(pkg.Scripts[name]) != "" {
					checks = append(checks, Check{ID: name, Command: "npm run " + name})
				}
			}
		}
	}
	if exists(filepath.Join(root, "go.mod")) {
		checks = append(checks, Check{ID: "go-test", Command: "go test ./..."})
	}
	if body, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil && strings.Contains(strings.ToLower(string(body)), "pytest") {
		checks = append(checks, Check{ID: "pytest", Command: "python -m pytest"})
	}
	if exists(filepath.Join(root, "Cargo.toml")) {
		checks = append(checks, Check{ID: "cargo-test", Command: "cargo test"})
	}
	return checks, nil
}

func ChangedFiles(ctx context.Context, runner Runner, root string) ([]string, error) {
	tracked, err := runner.Run(ctx, "git", "-C", root, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read tracked changes: %w", err)
	}
	untracked, err := runner.Run(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("read untracked changes: %w", err)
	}
	seen := map[string]bool{}
	var files []string
	for _, output := range [][]byte{tracked, untracked} {
		for name := range strings.SplitSeq(string(output), "\x00") {
			name = filepath.ToSlash(strings.TrimSpace(name))
			if name != "" && !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
		}
	}
	return files, nil
}

func Diff(ctx context.Context, runner Runner, root string) (string, error) {
	output, err := runner.Run(ctx, "git", "-C", root, "diff", "--no-ext-diff", "--binary", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(output), nil
}

func MatchesPath(path string, patterns []string) bool {
	path = filepath.ToSlash(path)
	for _, pattern := range patterns {
		clean := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(pattern)), "/")
		if path == clean || strings.HasPrefix(path, clean+"/") {
			return true
		}
	}
	return false
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("repository path must be relative: %q", path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("repository path escapes root: %q", path)
	}
	return nil
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func findAgentInstructions(root string) []string {
	var paths []string
	current := root
	for {
		path := filepath.Join(current, "AGENTS.md")
		if exists(path) {
			paths = append(paths, path)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return paths
}

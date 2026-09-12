package git

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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
	Root                  string   `json:"root"`
	SourceType            string   `json:"source_type"`
	Source                string   `json:"source"`
	BaseSHA               string   `json:"base_sha"`
	BranchName            string   `json:"branch_name"`
	Checks                []Check  `json:"checks"`
	Generated             []string `json:"generated"`
	Protected             []string `json:"protected"`
	Tests                 []string `json:"tests"`
	PreChangeVerification bool     `json:"pre_change_verification"`
	Instructions          []string `json:"instructions"`
}

var defaultTestPatterns = []string{"**/*_test.go", "**/*.test.ts", "**/*.spec.ts", "**/test_*.py", "tests/**"}

type directives struct {
	Checks                []Check  `yaml:"checks"`
	Generated             []string `yaml:"generated"`
	Protected             []string `yaml:"protected"`
	Tests                 []string `yaml:"tests"`
	PreChangeVerification bool     `yaml:"pre_change_verification"`
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
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return Profile{}, fmt.Errorf("resolve submitted repository path: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(root) {
		return Profile{}, fmt.Errorf("local repository path must exactly match the git root")
	}
	branch := materializationBranch(destination)
	if output, runErr := runner.Run(ctx, "git", "-C", root, "worktree", "add", "-b", branch, destination, sha); runErr != nil {
		return Profile{}, fmt.Errorf("create worktree: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	profile, err := profile(destination, "local", source, sha)
	if err != nil {
		return Profile{}, err
	}
	profile.Root = root
	profile.BranchName = branch
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
	branch := materializationBranch(destination)
	if output, switchErr := runner.Run(ctx, "git", "-C", destination, "switch", "-c", branch); switchErr != nil {
		return Profile{}, fmt.Errorf("create github work branch: %w: %s", switchErr, strings.TrimSpace(string(output)))
	}
	value, err := profile(destination, "github", repository, strings.TrimSpace(string(head)))
	if err != nil {
		return Profile{}, err
	}
	value.BranchName = branch
	return value, nil
}

func profile(workspace, sourceType, source, sha string) (Profile, error) {
	checks, generated, protected, tests, preChangeVerification, err := DetectQualityProfile(workspace)
	if err != nil {
		return Profile{}, err
	}
	instructions := findAgentInstructions(workspace)
	return Profile{Root: workspace, SourceType: sourceType, Source: source, BaseSHA: sha, Checks: checks, Generated: generated, Protected: protected, Tests: tests, PreChangeVerification: preChangeVerification, Instructions: instructions}, nil
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

func DetectQualityProfile(root string) ([]Check, []string, []string, []string, bool, error) {
	agents := filepath.Join(root, "AGENTS.md")
	if body, err := os.ReadFile(agents); err == nil {
		checks, generated, protected, tests, preChangeVerification, parseErr := parseDirectives(body)
		if parseErr != nil {
			return nil, nil, nil, nil, false, parseErr
		}
		return checks, generated, protected, tests, preChangeVerification, nil
	} else if !os.IsNotExist(err) {
		return nil, nil, nil, nil, false, err
	}
	checks, err := detectChecks(root)
	return checks, nil, nil, append([]string{}, defaultTestPatterns...), false, err
}

func parseDirectives(body []byte) ([]Check, []string, []string, []string, bool, error) {
	text := string(body)
	start := strings.Index(text, "<!-- software-factory:start -->")
	end := strings.Index(text, "<!-- software-factory:end -->")
	if start < 0 || end <= start {
		return nil, nil, nil, append([]string{}, defaultTestPatterns...), false, nil
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
		return nil, nil, nil, nil, false, fmt.Errorf("parse AGENTS.md factory block: %w", err)
	}
	seen := map[string]bool{}
	for _, check := range value.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Command) == "" || seen[check.ID] {
			return nil, nil, nil, nil, false, fmt.Errorf("checks require unique non-empty IDs and commands")
		}
		seen[check.ID] = true
	}
	for _, path := range append(append([]string{}, value.Generated...), value.Protected...) {
		if err := validateRelativePath(path); err != nil {
			return nil, nil, nil, nil, false, err
		}
	}
	tests := value.Tests
	if tests == nil {
		tests = append([]string{}, defaultTestPatterns...)
	}
	for _, pattern := range tests {
		if err := validateTestPattern(pattern); err != nil {
			return nil, nil, nil, nil, false, err
		}
	}
	return value.Checks, value.Generated, value.Protected, tests, value.PreChangeVerification, nil
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

func ChangedFiles(ctx context.Context, runner Runner, root, base string) ([]string, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("git change base is required")
	}
	tracked, err := runner.Run(ctx, "git", "-C", root, "diff", "--name-only", "-z", base, "--")
	if err != nil {
		return nil, fmt.Errorf("read tracked changes: %w", err)
	}
	untracked, err := runner.Run(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("read untracked changes: %w", err)
	}
	seen := map[string]bool{}
	files := make([]string, 0)
	for _, output := range [][]byte{tracked, untracked} {
		for name := range strings.SplitSeq(string(output), "\x00") {
			name = filepath.ToSlash(name)
			if name != "" && !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func Diff(ctx context.Context, runner Runner, root, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("git diff base is required")
	}
	output, err := runner.Run(ctx, "git", "-C", root, "diff", "--no-ext-diff", "--binary", base, "--")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(output), nil
}

// Fingerprint captures repository content and Git state relevant to a read-only turn.
func Fingerprint(ctx context.Context, runner Runner, root string) (string, error) {
	head, err := runner.Run(ctx, "git", "-C", root, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read repository head: %w", err)
	}
	tracked, err := runner.Run(ctx, "git", "-C", root, "ls-files", "--cached", "-z")
	if err != nil {
		return "", fmt.Errorf("read tracked files: %w", err)
	}
	untracked, err := runner.Run(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("read untracked files: %w", err)
	}
	staged, err := runner.Run(ctx, "git", "-C", root, "diff", "--cached", "--no-ext-diff", "--binary")
	if err != nil {
		return "", fmt.Errorf("read staged content: %w", err)
	}

	paths := make(map[string]struct{})
	for _, output := range [][]byte{tracked, untracked} {
		for path := range strings.SplitSeq(string(output), "\x00") {
			if path != "" {
				paths[filepath.FromSlash(path)] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	hasher := fnv.New128a()
	fmt.Fprintf(hasher, "head\x00%s\x00staged\x00", strings.TrimSpace(string(head)))
	fmt.Fprintf(hasher, "%x\x00", staged)
	for _, path := range ordered {
		info, statErr := os.Lstat(filepath.Join(root, path))
		if os.IsNotExist(statErr) {
			fmt.Fprintf(hasher, "path\x00%s\x00missing\x00", filepath.ToSlash(path))
			continue
		}
		if statErr != nil {
			return "", fmt.Errorf("stat repository path %s: %w", path, statErr)
		}
		fmt.Fprintf(hasher, "path\x00%s\x00mode\x00%o\x00", filepath.ToSlash(path), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(filepath.Join(root, path))
			if readErr != nil {
				return "", fmt.Errorf("read symlink %s: %w", path, readErr)
			}
			fmt.Fprintf(hasher, "symlink\x00%s\x00", target)
			continue
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil {
			return "", fmt.Errorf("read repository path %s: %w", path, readErr)
		}
		fmt.Fprintf(hasher, "content\x00%x\x00", body)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// Commit stages and commits exactly paths, leaving unrelated index entries alone.
func Commit(ctx context.Context, runner Runner, root, message string, paths []string) (string, error) {
	validated := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if err := validateRelativePath(path); err != nil {
			return "", err
		}
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == "." {
			return "", fmt.Errorf("repository path must name a file: %q", path)
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean))); err == nil && info.IsDir() {
			return "", fmt.Errorf("repository path must name a file: %q", path)
		}
		if _, exists := seen[clean]; !exists {
			seen[clean] = struct{}{}
			validated = append(validated, clean)
		}
	}
	if len(validated) == 0 {
		return currentHead(ctx, runner, root)
	}
	sort.Strings(validated)
	addArgs := []string{"--literal-pathspecs", "-C", root, "add", "--force", "--"}
	addArgs = append(addArgs, validated...)
	if output, err := runner.Run(ctx, "git", addArgs...); err != nil {
		return "", fmt.Errorf("stage factory paths: %w: %s", err, strings.TrimSpace(string(output)))
	}
	checkArgs := []string{"--literal-pathspecs", "-C", root, "diff", "--cached", "--name-only", "-z", "HEAD", "--"}
	checkArgs = append(checkArgs, validated...)
	output, err := runner.Run(ctx, "git", checkArgs...)
	if err != nil {
		return "", fmt.Errorf("check staged factory paths: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) == 0 {
		return currentHead(ctx, runner, root)
	}
	commitArgs := []string{"-C", root, "-c", "user.name=Software Factory", "-c", "user.email=software-factory@localhost", "commit", "--only", "-m", message, "--"}
	commitArgs = append(commitArgs, validated...)
	if output, err := runner.Run(ctx, "git", commitArgs...); err != nil {
		return "", fmt.Errorf("commit factory paths: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return currentHead(ctx, runner, root)
}

func RetainRef(ctx context.Context, runner Runner, root, ref, sha string) error {
	if !strings.HasPrefix(ref, "refs/software-factory/") || strings.ContainsAny(ref, " \t\r\n") || sha == "" {
		return fmt.Errorf("invalid retained Git ref")
	}
	if output, err := runner.Run(ctx, "git", "-C", root, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("retain Git ref: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// RestoreForRetry creates a fresh execution branch at the recorded Git input.
func RestoreForRetry(ctx context.Context, runner Runner, sourceType, canonical, working, head, branch string) error {
	if head == "" || branch == "" {
		return fmt.Errorf("retry Git state requires head and branch")
	}
	if sourceType == "local" {
		if output, err := runner.Run(ctx, "git", "-C", canonical, "worktree", "remove", "--force", working); err != nil {
			return fmt.Errorf("remove retry worktree: %w: %s", err, strings.TrimSpace(string(output)))
		}
		if output, err := runner.Run(ctx, "git", "-C", canonical, "worktree", "add", "-b", branch, working, head); err != nil {
			return fmt.Errorf("create retry worktree: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	backup := working + ".software-factory-source"
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("clear retry clone backup: %w", err)
	}
	if err := os.Rename(working, backup); err != nil {
		return fmt.Errorf("move retry clone: %w", err)
	}
	restore := func() {
		_ = os.RemoveAll(working)
		_ = os.Rename(backup, working)
	}
	if output, err := runner.Run(ctx, "git", "clone", "--local", backup, working); err != nil {
		restore()
		return fmt.Errorf("clone retry repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runner.Run(ctx, "git", "-C", working, "switch", "-c", branch, head); err != nil {
		restore()
		return fmt.Errorf("create retry clone branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove retry clone backup: %w", err)
	}
	return nil
}

func Head(ctx context.Context, runner Runner, root string) (string, error) {
	return currentHead(ctx, runner, root)
}

func Branch(ctx context.Context, runner Runner, root string) (string, error) {
	output, err := runner.Run(ctx, "git", "-C", root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read Git branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func currentHead(ctx context.Context, runner Runner, root string) (string, error) {
	output, err := runner.Run(ctx, "git", "-C", root, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read Git head: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func materializationBranch(destination string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(filepath.Clean(destination)))
	return fmt.Sprintf("software-factory/%08x", hash.Sum32())
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

func MatchesGlob(file string, patterns []string) bool {
	file = filepath.ToSlash(file)
	for _, pattern := range patterns {
		if globSegments(strings.Split(strings.Trim(filepath.ToSlash(pattern), "/"), "/"), strings.Split(file, "/")) {
			return true
		}
	}
	return false
}

func globSegments(pattern, file []string) bool {
	if len(pattern) == 0 {
		return len(file) == 0
	}
	if pattern[0] == "**" {
		return globSegments(pattern[1:], file) || len(file) > 0 && globSegments(pattern, file[1:])
	}
	if len(file) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], file[0])
	return err == nil && matched && globSegments(pattern[1:], file[1:])
}

func validateTestPattern(pattern string) error {
	if pattern == "" || filepath.IsAbs(pattern) || strings.HasPrefix(pattern, ":") {
		return fmt.Errorf("test pattern must be relative: %q", pattern)
	}
	normalized := filepath.ToSlash(pattern)
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("test pattern escapes root: %q", pattern)
		}
	}
	if _, err := path.Match("", ""); err != nil {
		return fmt.Errorf("invalid test pattern %q: %w", pattern, err)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment != "**" {
			if _, err := path.Match(segment, ""); err != nil {
				return fmt.Errorf("invalid test pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

type Change struct {
	Path       string
	Kind       string
	RenameFrom string
	RenameTo   string
}

func ChangedEntries(ctx context.Context, runner Runner, root, base string) ([]Change, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("git change base is required")
	}
	tracked, err := runner.Run(ctx, "git", "-C", root, "diff", "--name-status", "--find-renames", "-z", base, "--")
	if err != nil {
		return nil, fmt.Errorf("read tracked change entries: %w", err)
	}
	changes := make([]Change, 0)
	seen := map[string]bool{}
	fields := strings.Split(string(tracked), "\x00")
	for index := 0; index < len(fields); {
		if fields[index] == "" {
			index++
			continue
		}
		status := fields[index]
		index++
		if index >= len(fields) {
			break
		}
		first := filepath.ToSlash(fields[index])
		index++
		change := Change{Path: first, Kind: changeKind(status)}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(fields) {
				break
			}
			change.RenameFrom = first
			change.Path = filepath.ToSlash(fields[index])
			index++
			if !seen[change.RenameFrom] {
				changes = append(changes, Change{Path: change.RenameFrom, Kind: "renamed", RenameFrom: change.RenameFrom, RenameTo: change.Path})
				seen[change.RenameFrom] = true
			}
		}
		if !seen[change.Path] {
			changes = append(changes, change)
			seen[change.Path] = true
		}
	}
	untracked, err := runner.Run(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("read untracked change entries: %w", err)
	}
	for _, file := range strings.Split(string(untracked), "\x00") {
		file = filepath.ToSlash(file)
		if file != "" && !seen[file] {
			changes = append(changes, Change{Path: file, Kind: "added"})
			seen[file] = true
		}
	}
	sort.Slice(changes, func(left, right int) bool { return changes[left].Path < changes[right].Path })
	return changes, nil
}

func changeKind(status string) string {
	switch status[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	default:
		return "modified"
	}
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, ":") {
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

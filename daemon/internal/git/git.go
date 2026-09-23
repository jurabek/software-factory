package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/utils/merkletrie"
	xworktree "github.com/go-git/go-git/v6/x/plumbing/worktree"
	"gopkg.in/yaml.v3"
)

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

// open resolves a repository from any path inside its working tree.
func open(root string) (*gogit.Repository, error) {
	repository, err := gogit.PlainOpenWithOptions(root, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open git repository: %w", err)
	}
	return repository, nil
}

// resolveRoot returns the canonical repository root and its current HEAD.
func resolveRoot(path string) (string, string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("repository path is not a directory")
	}
	repository, err := open(resolved)
	if err != nil {
		return "", "", fmt.Errorf("find git root: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return "", "", fmt.Errorf("open git worktree: %w", err)
	}
	root, err := filepath.EvalSymlinks(worktree.Filesystem().Root())
	if err != nil {
		return "", "", fmt.Errorf("canonical git root: %w", err)
	}
	head, err := repository.Head()
	if err != nil {
		return "", "", fmt.Errorf("read git head: %w", err)
	}
	return root, head.Hash().String(), nil
}

// PrepareLocal materializes a linked worktree of the local repository.
func PrepareLocal(source, destination string) (Profile, error) {
	root, sha, err := resolveRoot(source)
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
	if err = addLinkedWorktree(root, destination, sha); err != nil {
		return Profile{}, err
	}
	value, err := buildProfile(destination, "local", source, sha)
	if err != nil {
		return Profile{}, err
	}
	value.Root = root
	value.BranchName = worktreeName(destination)
	return value, nil
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// PrepareGitHub clones a GitHub repository and checks out the materialization
// branch. Authentication uses GITHUB_TOKEN or GH_TOKEN when present.
func PrepareGitHub(ctx context.Context, repository, destination string) (Profile, error) {
	if !githubRepository.MatchString(repository) {
		return Profile{}, fmt.Errorf("github repository must be owner/repository")
	}
	options := &gogit.CloneOptions{URL: "https://github.com/" + repository + ".git"}
	if auth := githubToken(); auth != nil {
		options.ClientOptions = append(options.ClientOptions, client.WithHTTPAuth(auth))
	}
	cloned, err := gogit.PlainCloneContext(ctx, destination, options)
	if err != nil {
		return Profile{}, fmt.Errorf("clone github repository: %w", err)
	}
	head, err := cloned.Head()
	if err != nil {
		return Profile{}, fmt.Errorf("read cloned head: %w", err)
	}
	branch := worktreeName(destination)
	worktree, err := cloned.Worktree()
	if err != nil {
		return Profile{}, fmt.Errorf("open cloned worktree: %w", err)
	}
	if err = worktree.Checkout(&gogit.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Create: true}); err != nil {
		return Profile{}, fmt.Errorf("create github work branch: %w", err)
	}
	value, err := buildProfile(destination, "github", repository, head.Hash().String())
	if err != nil {
		return Profile{}, err
	}
	value.BranchName = branch
	return value, nil
}

func githubToken() *githttp.BasicAuth {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return &githttp.BasicAuth{Username: "x-access-token", Password: token}
		}
	}
	return nil
}

// RemoveWorktree removes a linked worktree and its files. Missing worktrees are
// ignored so repeated cleanup stays idempotent.
func RemoveWorktree(canonical, working string) error {
	manager, err := worktreeManager(canonical)
	if err != nil {
		return err
	}
	if err = manager.Remove(worktreeName(working)); err != nil && !errors.Is(err, xworktree.ErrWorktreeNotFound) {
		return fmt.Errorf("remove worktree metadata: %w", err)
	}
	return os.RemoveAll(working)
}

func addLinkedWorktree(canonical, destination, sha string) error {
	manager, err := worktreeManager(canonical)
	if err != nil {
		return err
	}
	hash := plumbing.NewHash(sha)
	if err = os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create worktree directory: %w", err)
	}
	if err = manager.Add(osfs.New(destination), worktreeName(destination), xworktree.WithCommit(hash)); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	return nil
}

func worktreeManager(canonical string) (*xworktree.Worktree, error) {
	storer := filesystem.NewStorage(osfs.New(filepath.Join(canonical, ".git")), cache.NewObjectLRUDefault())
	manager, err := xworktree.New(storer)
	if err != nil {
		return nil, fmt.Errorf("open worktree manager: %w", err)
	}
	return manager, nil
}

func buildProfile(workspace, sourceType, source, sha string) (Profile, error) {
	checks, generated, protected, tests, preChangeVerification, err := DetectQualityProfile(workspace)
	if err != nil {
		return Profile{}, err
	}
	instructions := findAgentInstructions(workspace)
	return Profile{Root: workspace, SourceType: sourceType, Source: source, BaseSHA: sha, Checks: checks, Generated: generated, Protected: protected, Tests: tests, PreChangeVerification: preChangeVerification, Instructions: instructions}, nil
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

// ChangedFiles lists repository-relative paths changed since base, including
// untracked files.
func ChangedFiles(root, base string) ([]string, error) {
	entries, err := ChangedEntries(root, base)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.Path)
	}
	return files, nil
}

// Diff renders the unified patch from base to the current working tree.
func Diff(root, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("git diff base is required")
	}
	repository, err := open(root)
	if err != nil {
		return "", err
	}
	baseTree, err := baseTreeFor(repository, base)
	if err != nil {
		return "", err
	}
	entries, err := changedEntries(repository, baseTree)
	if err != nil {
		return "", err
	}
	changes := make(object.Changes, 0, len(entries))
	for _, entry := range entries {
		change, changeErr := worktreeChange(repository, baseTree, root, entry.Path)
		if changeErr != nil {
			return "", changeErr
		}
		if change != nil {
			changes = append(changes, change)
		}
	}
	patch, err := changes.PatchContext(context.Background())
	if err != nil {
		return "", fmt.Errorf("encode git diff: %w", err)
	}
	return patch.String(), nil
}

// worktreeChange builds a tree-to-worktree change for one path. The provided
// tree only supplies the object storer to the patch encoder.
func worktreeChange(repository *gogit.Repository, baseTree *object.Tree, root, file string) (*object.Change, error) {
	clean := filepath.ToSlash(filepath.Clean(file))
	if clean == "." || clean == "" {
		return nil, nil
	}
	change := &object.Change{}
	from, err := baseTree.FindEntry(clean)
	if err == nil {
		change.From = object.ChangeEntry{Name: clean, Tree: baseTree, TreeEntry: *from}
	} else if !errors.Is(err, object.ErrEntryNotFound) && !errors.Is(err, object.ErrFileNotFound) {
		return nil, fmt.Errorf("read diff base entry %s: %w", clean, err)
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	info, err := os.Lstat(full)
	if err == nil {
		hash, mode, blobErr := writeBlob(repository, full, info)
		if blobErr != nil {
			return nil, blobErr
		}
		change.To = object.ChangeEntry{Name: clean, Tree: baseTree, TreeEntry: object.TreeEntry{Name: path.Base(clean), Mode: mode, Hash: hash}}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read worktree path %s: %w", clean, err)
	}
	if change.From.Name == "" && change.To.Name == "" {
		return nil, nil
	}
	return change, nil
}

func writeBlob(repository *gogit.Repository, file string, info os.FileInfo) (plumbing.Hash, filemode.FileMode, error) {
	mode := worktreeFileMode(info)
	content, err := readWorktreeContent(file, info)
	if err != nil {
		return plumbing.ZeroHash, mode, err
	}
	encoded := repository.Storer.NewEncodedObject()
	encoded.SetType(plumbing.BlobObject)
	writer, err := encoded.Writer()
	if err != nil {
		return plumbing.ZeroHash, mode, fmt.Errorf("encode worktree blob: %w", err)
	}
	if _, err = writer.Write(content); err != nil {
		writer.Close()
		return plumbing.ZeroHash, mode, fmt.Errorf("encode worktree blob: %w", err)
	}
	if err = writer.Close(); err != nil {
		return plumbing.ZeroHash, mode, fmt.Errorf("encode worktree blob: %w", err)
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		return plumbing.ZeroHash, mode, fmt.Errorf("store worktree blob: %w", err)
	}
	return hash, mode, nil
}

func worktreeFileMode(info os.FileInfo) filemode.FileMode {
	if info.Mode()&os.ModeSymlink != 0 {
		return filemode.Symlink
	}
	if info.Mode().Perm()&0o111 != 0 {
		return filemode.Executable
	}
	return filemode.Regular
}

func readWorktreeContent(file string, info os.FileInfo) ([]byte, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(file)
		if err != nil {
			return nil, fmt.Errorf("read worktree symlink %s: %w", file, err)
		}
		return []byte(target), nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read worktree path %s: %w", file, err)
	}
	return content, nil
}

func baseTreeFor(repository *gogit.Repository, base string) (*object.Tree, error) {
	baseCommit, err := repository.CommitObject(plumbing.NewHash(base))
	if err != nil {
		return nil, fmt.Errorf("read change base: %w", err)
	}
	tree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read change base tree: %w", err)
	}
	return tree, nil
}

// Fingerprint captures repository content and Git state relevant to a read-only turn.
func Fingerprint(root string) (string, error) {
	repository, err := open(root)
	if err != nil {
		return "", err
	}
	head, err := repository.Head()
	if err != nil {
		return "", fmt.Errorf("read repository head: %w", err)
	}
	index, err := repository.Storer.Index()
	if err != nil {
		return "", fmt.Errorf("read repository index: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return "", fmt.Errorf("open git worktree: %w", err)
	}
	status, err := worktree.Status()
	if err != nil {
		return "", fmt.Errorf("read repository status: %w", err)
	}
	paths := make(map[string]struct{}, len(index.Entries))
	for _, entry := range index.Entries {
		paths[filepath.ToSlash(entry.Name)] = struct{}{}
	}
	for file, state := range status {
		if state.Worktree == gogit.Untracked {
			paths[filepath.ToSlash(file)] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(paths))
	for file := range paths {
		ordered = append(ordered, file)
	}
	sort.Strings(ordered)

	hasher := fnv.New128a()
	fmt.Fprintf(hasher, "head\x00%s\x00", head.Hash().String())
	for _, entry := range index.Entries {
		fmt.Fprintf(hasher, "index\x00%s\x00%o\x00%s\x00", filepath.ToSlash(entry.Name), entry.Mode, entry.Hash.String())
	}
	for _, file := range ordered {
		info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(file)))
		if os.IsNotExist(statErr) {
			fmt.Fprintf(hasher, "path\x00%s\x00missing\x00", file)
			continue
		}
		if statErr != nil {
			return "", fmt.Errorf("stat repository path %s: %w", file, statErr)
		}
		fmt.Fprintf(hasher, "path\x00%s\x00mode\x00%o\x00", file, info.Mode())
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		body, readErr := readWorktreeContent(filepath.Join(root, filepath.FromSlash(file)), info)
		if readErr != nil {
			return "", readErr
		}
		fmt.Fprintf(hasher, "content\x00%x\x00", body)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
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
	if slices.Contains(strings.Split(normalized, "/"), "..") {
		return fmt.Errorf("test pattern escapes root: %q", pattern)
	}
	if _, err := path.Match("", ""); err != nil {
		return fmt.Errorf("invalid test pattern %q: %w", pattern, err)
	}
	for segment := range strings.SplitSeq(normalized, "/") {
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

// ChangedEntries lists committed and uncommitted changes since base. Renames
// surface as both the previous and resulting path.
func ChangedEntries(root, base string) ([]Change, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("git change base is required")
	}
	repository, err := open(root)
	if err != nil {
		return nil, err
	}
	baseTree, err := baseTreeFor(repository, base)
	if err != nil {
		return nil, err
	}
	return changedEntries(repository, baseTree)
}

func changedEntries(repository *gogit.Repository, baseTree *object.Tree) ([]Change, error) {
	headRef, err := repository.Head()
	if err != nil {
		return nil, fmt.Errorf("read repository head: %w", err)
	}
	headCommit, err := repository.CommitObject(headRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("read repository head commit: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read repository head tree: %w", err)
	}
	committed, err := object.DiffTreeWithOptions(context.Background(), baseTree, headTree, object.DefaultDiffTreeOptions)
	if err != nil {
		return nil, fmt.Errorf("diff committed changes: %w", err)
	}

	changes := make([]Change, 0)
	seen := map[string]bool{}
	add := func(file, kind, from, to string) {
		file = filepath.ToSlash(file)
		if file == "" || seen[file] {
			return
		}
		seen[file] = true
		changes = append(changes, Change{Path: file, Kind: kind, RenameFrom: from, RenameTo: to})
	}
	for _, change := range committed {
		action, actionErr := change.Action()
		if actionErr != nil {
			return nil, actionErr
		}
		if change.From.Name != "" && change.To.Name != "" && change.From.Name != change.To.Name {
			add(change.To.Name, "renamed", change.From.Name, change.To.Name)
			add(change.From.Name, "renamed", change.From.Name, change.To.Name)
			continue
		}
		switch action {
		case merkletrie.Insert:
			add(change.To.Name, "added", "", "")
		case merkletrie.Delete:
			add(change.From.Name, "deleted", "", "")
		default:
			add(change.To.Name, "modified", "", "")
		}
	}

	worktree, err := repository.Worktree()
	if err != nil {
		return nil, fmt.Errorf("open git worktree: %w", err)
	}
	status, err := worktree.Status()
	if err != nil {
		return nil, fmt.Errorf("read repository status: %w", err)
	}
	for file, state := range status {
		if state.Staging == gogit.Unmodified && state.Worktree == gogit.Unmodified {
			continue
		}
		if state.Worktree == gogit.Untracked && state.Staging == gogit.Untracked {
			add(file, "added", "", "")
			continue
		}
		if state.Staging == gogit.Renamed && state.Extra != "" {
			add(file, "renamed", filepath.ToSlash(state.Extra), filepath.ToSlash(file))
			add(state.Extra, "renamed", filepath.ToSlash(state.Extra), filepath.ToSlash(file))
			continue
		}
		kind := "modified"
		switch {
		case state.Staging == gogit.Deleted || state.Worktree == gogit.Deleted:
			kind = "deleted"
		case state.Staging == gogit.Added:
			kind = "added"
		}
		add(file, kind, "", "")
	}

	sort.Slice(changes, func(left, right int) bool { return changes[left].Path < changes[right].Path })
	return changes, nil
}

// worktreeName derives a worktree and branch name that satisfies go-git's name
// grammar while staying unique per materialization target.
func worktreeName(destination string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(filepath.Clean(destination)))
	return fmt.Sprintf("software-factory-%08x", hash.Sum32())
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

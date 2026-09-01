package gitview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Sub-repository discovery: find independent git repositories nested under a
// managed root, so a work directory holding several checkouts can show each
// repository's changes instead of reporting "not a git repository".
//
// The scan is filesystem-only (no git process per candidate) because Windows
// pays a noticeable cost per spawned process and the scan runs on every sidebar
// refresh. Branch and head come from reading .git/HEAD directly.
//
// Never reported: the scanned root itself (callers already know its status via
// HasRepo), submodules (their superproject owns the status), and linked
// worktrees of a repository inside the root (ListWorktrees already surfaces
// those). Symlinked directories are not followed, to avoid cycles.

const (
	defaultSubRepoMaxDepth = 4
	defaultSubRepoMaxItems = 64
	// Bounds the worst case when a root turns out to be a huge tree (a home
	// directory, a whole volume). MaxItems alone does not help there: a tree with
	// no repositories at all would still be walked in full.
	defaultSubRepoMaxDirs = 4096
)

// Directory names that never hold a project-level repository worth listing.
// Kept local rather than shared with the watcher's ignore set so this file stays
// self-contained. Names starting with "." are skipped separately.
var subRepoSkipDirs = map[string]struct{}{
	"__pycache__":  {},
	"bin":          {},
	"build":        {},
	"coverage":     {},
	"dist":         {},
	"node_modules": {},
	"obj":          {},
	"out":          {},
	"pods":         {},
	"target":       {},
	"tmp":          {},
	"vendor":       {},
	"venv":         {},
}

// SubRepoItem describes one independent repository found under a managed root.
type SubRepoItem struct {
	// Path is the absolute repository root.
	Path string `json:"path"`
	// RelPath is Path relative to the scanned root, slash-separated.
	RelPath string `json:"rel_path"`
	// Name is the repository directory name.
	Name string `json:"name"`
	// Branch is the checked out branch, empty when detached.
	Branch string `json:"branch,omitempty"`
	// Head is the resolved HEAD commit when it can be read cheaply.
	Head string `json:"head,omitempty"`
}

type SubRepoListResult struct {
	Items []SubRepoItem `json:"items"`
	// Truncated reports that scanning stopped at the item limit.
	Truncated bool `json:"truncated,omitempty"`
}

// SubRepoScanOptions tunes ListSubRepos. Zero values fall back to defaults.
type SubRepoScanOptions struct {
	// MaxDepth limits how many directory levels below rootPath are scanned.
	// Depth 1 means direct children of rootPath.
	MaxDepth int
	// MaxItems caps the number of reported repositories.
	MaxItems int
	// MaxDirs caps how many directories are visited before the scan gives up.
	MaxDirs int
	// ExcludePaths are absolute paths whose subtrees are skipped entirely.
	// Callers pass known linked worktrees so those are not reported twice.
	ExcludePaths []string
}

// ListSubRepos walks rootPath looking for nested independent repositories.
//
// A directory qualifies when it holds a .git directory, or a .git file whose
// gitdir target lives outside rootPath (a linked worktree of an external
// repository, which the root's own worktree list cannot surface). Submodules and
// worktrees of repositories inside rootPath are skipped.
//
// Descent stops at a discovered repository: nested repositories inside another
// repository belong to that repository's own view.
func ListSubRepos(ctx context.Context, rootPath string, opts SubRepoScanOptions) (SubRepoListResult, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return SubRepoListResult{}, errors.New("root path required")
	}
	rootPath = filepath.Clean(rootPath)
	if resolved, err := filepath.EvalSymlinks(rootPath); err == nil {
		rootPath = filepath.Clean(resolved)
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return SubRepoListResult{}, err
	}
	if !info.IsDir() {
		return SubRepoListResult{}, errors.New("root path must be a directory")
	}

	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultSubRepoMaxDepth
	}
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = defaultSubRepoMaxItems
	}
	maxDirs := opts.MaxDirs
	if maxDirs <= 0 {
		maxDirs = defaultSubRepoMaxDirs
	}
	excluded := make(map[string]struct{}, len(opts.ExcludePaths))
	for _, path := range opts.ExcludePaths {
		if key := subRepoPathKey(path); key != "" {
			excluded[key] = struct{}{}
		}
	}

	result := SubRepoListResult{Items: make([]SubRepoItem, 0, 8)}
	scan := &subRepoScanner{
		rootPath: rootPath,
		maxDepth: maxDepth,
		maxItems: maxItems,
		maxDirs:  maxDirs,
		excluded: excluded,
	}
	if err := scan.walk(ctx, rootPath, 1, &result); err != nil {
		return SubRepoListResult{}, err
	}
	sort.Slice(result.Items, func(i, j int) bool {
		return result.Items[i].RelPath < result.Items[j].RelPath
	})
	return result, nil
}

type subRepoScanner struct {
	rootPath    string
	maxDepth    int
	maxItems    int
	maxDirs     int
	visitedDirs int
	excluded    map[string]struct{}
}

func (s *subRepoScanner) walk(ctx context.Context, dir string, depth int, result *SubRepoListResult) error {
	if depth > s.maxDepth {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.visitedDirs++
	if s.visitedDirs > s.maxDirs {
		result.Truncated = true
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// An unreadable directory must not fail the whole scan; the rest of the
		// tree is still useful.
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if skipSubRepoDirName(name) {
			continue
		}
		childPath := filepath.Join(dir, name)
		if _, ok := s.excluded[subRepoPathKey(childPath)]; ok {
			continue
		}
		// Do not follow symlinked directories: they can point back into the tree
		// and turn the scan into a cycle.
		if info, err := entry.Info(); err == nil && info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if len(result.Items) >= s.maxItems {
			result.Truncated = true
			return nil
		}
		if item, ok := s.inspect(childPath); ok {
			result.Items = append(result.Items, item)
			// Stop descending: anything nested belongs to this repository.
			continue
		}
		if err := s.walk(ctx, childPath, depth+1, result); err != nil {
			return err
		}
		if result.Truncated {
			return nil
		}
	}
	return nil
}

// inspect reports whether dir is an independent repository root.
func (s *subRepoScanner) inspect(dir string) (SubRepoItem, bool) {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return SubRepoItem{}, false
	}
	gitDir := ""
	switch {
	case info.IsDir():
		gitDir = gitPath
	case info.Mode().IsRegular():
		target, ok := readGitDirFile(gitPath)
		if !ok {
			return SubRepoItem{}, false
		}
		// A .git file points at a real git dir: either a submodule (inside the
		// superproject, which owns it) or a linked worktree. Both are only worth
		// reporting when the git dir lives outside the scanned root, meaning the
		// owning repository is not represented here.
		if subRepoPathInside(target, s.rootPath) {
			return SubRepoItem{}, false
		}
		gitDir = target
	default:
		return SubRepoItem{}, false
	}

	relPath, err := filepath.Rel(s.rootPath, dir)
	if err != nil {
		return SubRepoItem{}, false
	}
	branch, head := readGitHead(gitDir)
	return SubRepoItem{
		Path:    dir,
		RelPath: filepath.ToSlash(relPath),
		Name:    filepath.Base(dir),
		Branch:  branch,
		Head:    head,
	}, true
}

// readGitDirFile resolves the target of a ".git" file ("gitdir: <path>").
func readGitDirFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			continue
		}
		target := strings.TrimSpace(rest)
		if target == "" {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return filepath.Clean(target), true
	}
	return "", false
}

// readGitHead reads branch and commit from a git dir without spawning git.
// Either value may come back empty (detached HEAD, unborn branch, packed refs);
// callers treat them as display hints only.
func readGitHead(gitDir string) (branch string, head string) {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	content := strings.TrimSpace(string(data))
	ref, ok := strings.CutPrefix(content, "ref:")
	if !ok {
		// Detached HEAD stores the commit id directly.
		if isHexCommitID(content) {
			return "", content
		}
		return "", ""
	}
	ref = strings.TrimSpace(ref)
	branch = strings.TrimPrefix(ref, "refs/heads/")
	if refData, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		candidate := strings.TrimSpace(string(refData))
		if isHexCommitID(candidate) {
			head = candidate
		}
	}
	// A missing loose ref means the commit sits in packed-refs; branch alone is
	// enough for display, so no fallback lookup here.
	return branch, head
}

func isHexCommitID(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		isDigit := char >= '0' && char <= '9'
		isLower := char >= 'a' && char <= 'f'
		isUpper := char >= 'A' && char <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

func skipSubRepoDirName(name string) bool {
	if name == "" {
		return true
	}
	// Dot directories hold tooling state, not projects. This also covers ".git"
	// itself and the task worktree parent ".worktree".
	if strings.HasPrefix(name, ".") {
		return true
	}
	key := name
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	_, skip := subRepoSkipDirs[key]
	return skip
}

func subRepoPathKey(path string) string {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return ""
	}
	cleaned = filepath.Clean(cleaned)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

// subRepoPathInside reports whether path is dir itself or nested under it.
func subRepoPathInside(path, dir string) bool {
	pathKey := subRepoPathKey(path)
	dirKey := subRepoPathKey(dir)
	if pathKey == "" || dirKey == "" {
		return false
	}
	if pathKey == dirKey {
		return true
	}
	rel, err := filepath.Rel(dirKey, pathKey)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

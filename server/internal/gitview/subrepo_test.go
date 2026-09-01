package gitview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListSubReposFindsNestedRepositories(t *testing.T) {
	root := t.TempDir()
	makeBareGitDir(t, filepath.Join(root, "web"))
	makeBareGitDir(t, filepath.Join(root, "services", "api"))
	writeTestFile(t, root, "notes/readme.md", "plain dir\n")

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	want := []string{"services/api", "web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rel paths = %v, want %v", got, want)
	}
	if result.Truncated {
		t.Fatal("Truncated = true, want false")
	}
	for _, item := range result.Items {
		if !filepath.IsAbs(item.Path) {
			t.Fatalf("Path %q is not absolute", item.Path)
		}
		if item.Name == "" {
			t.Fatalf("Name empty for %q", item.RelPath)
		}
	}
}

func TestListSubReposExcludesScannedRoot(t *testing.T) {
	root := t.TempDir()
	makeBareGitDir(t, root)
	makeBareGitDir(t, filepath.Join(root, "child"))

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	if strings.Join(got, ",") != "child" {
		t.Fatalf("rel paths = %v, want [child]", got)
	}
}

func TestListSubReposStopsAtDiscoveredRepository(t *testing.T) {
	root := t.TempDir()
	makeBareGitDir(t, filepath.Join(root, "outer"))
	makeBareGitDir(t, filepath.Join(root, "outer", "inner"))

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	if strings.Join(got, ",") != "outer" {
		t.Fatalf("rel paths = %v, want [outer]; nested repo must not be reported", got)
	}
}

func TestListSubReposSkipsSubmoduleAndInternalWorktree(t *testing.T) {
	root := t.TempDir()
	// Submodule: .git file pointing into the superproject's git dir, which lives
	// inside the scanned root.
	superGitDir := filepath.Join(root, "super", ".git")
	if err := os.MkdirAll(filepath.Join(superGitDir, "modules", "lib"), 0o755); err != nil {
		t.Fatalf("mkdir super modules: %v", err)
	}
	writeTestFile(t, root, "super/.git/HEAD", "ref: refs/heads/main\n")
	submodulePath := filepath.Join(root, "super", "lib")
	if err := os.MkdirAll(submodulePath, 0o755); err != nil {
		t.Fatalf("mkdir submodule: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(submodulePath, ".git"),
		[]byte("gitdir: "+filepath.Join(superGitDir, "modules", "lib")+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write submodule .git: %v", err)
	}

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	if strings.Join(got, ",") != "super" {
		t.Fatalf("rel paths = %v, want [super]; submodule must not be reported separately", got)
	}
}

func TestListSubReposReportsExternalLinkedWorktree(t *testing.T) {
	// A .git file pointing outside the scanned root belongs to a repository the
	// root cannot otherwise surface, so it is worth reporting.
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	external := filepath.Join(base, "external-repo", ".git", "worktrees", "feature")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatalf("write external HEAD: %v", err)
	}
	linked := filepath.Join(root, "feature-checkout")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatalf("mkdir linked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+external+"\n"), 0o644); err != nil {
		t.Fatalf("write linked .git: %v", err)
	}

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	if strings.Join(got, ",") != "feature-checkout" {
		t.Fatalf("rel paths = %v, want [feature-checkout]", got)
	}
	if result.Items[0].Branch != "feature" {
		t.Fatalf("Branch = %q, want feature", result.Items[0].Branch)
	}
}

func TestListSubReposHonoursExcludePaths(t *testing.T) {
	root := t.TempDir()
	makeBareGitDir(t, filepath.Join(root, "keep"))
	makeBareGitDir(t, filepath.Join(root, "skip"))

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{
		ExcludePaths: []string{filepath.Join(root, "skip")},
	})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	got := subRepoRelPaths(result)
	if strings.Join(got, ",") != "keep" {
		t.Fatalf("rel paths = %v, want [keep]", got)
	}
}

func TestListSubReposSkipsNoiseDirectoriesAndRespectsDepth(t *testing.T) {
	root := t.TempDir()
	makeBareGitDir(t, filepath.Join(root, "node_modules", "pkg"))
	makeBareGitDir(t, filepath.Join(root, ".worktree", "task-1"))
	makeBareGitDir(t, filepath.Join(root, "a", "b", "c", "deep"))

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{MaxDepth: 2})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("items = %v, want none", subRepoRelPaths(result))
	}

	deep, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{MaxDepth: 4})
	if err != nil {
		t.Fatalf("ListSubRepos deep: %v", err)
	}
	got := subRepoRelPaths(deep)
	if strings.Join(got, ",") != "a/b/c/deep" {
		t.Fatalf("rel paths = %v, want [a/b/c/deep]", got)
	}
}

func TestListSubReposTruncatesAtMaxItems(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		makeBareGitDir(t, filepath.Join(root, name))
	}

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{MaxItems: 2})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(result.Items))
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestListSubReposReadsBranchAndHead(t *testing.T) {
	root := initTestRepo(t)
	nested := filepath.Join(root, "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	runTestGit(t, nested, "init")
	runTestGit(t, nested, "config", "user.email", "test@example.com")
	runTestGit(t, nested, "config", "user.name", "Test User")
	writeTestFile(t, nested, "note.txt", "hello\n")
	runTestGit(t, nested, "add", "note.txt")
	runTestGit(t, nested, "commit", "-m", "initial")
	runTestGit(t, nested, "checkout", "-b", "feature")
	wantHead := strings.TrimSpace(runTestGit(t, nested, "rev-parse", "HEAD"))

	result, err := ListSubRepos(context.Background(), root, SubRepoScanOptions{})
	if err != nil {
		t.Fatalf("ListSubRepos: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %v, want [child]", subRepoRelPaths(result))
	}
	item := result.Items[0]
	if item.Branch != "feature" {
		t.Fatalf("Branch = %q, want feature", item.Branch)
	}
	if item.Head != wantHead {
		t.Fatalf("Head = %q, want %q", item.Head, wantHead)
	}
	// The discovered path must be usable by the existing path-based git APIs.
	status, err := InspectStatus(context.Background(), item.Path)
	if err != nil {
		t.Fatalf("InspectStatus on discovered repo: %v", err)
	}
	if !status.Available {
		t.Fatal("InspectStatus reported unavailable for discovered repo")
	}
}

func TestListSubReposRejectsMissingRoot(t *testing.T) {
	if _, err := ListSubRepos(context.Background(), "", SubRepoScanOptions{}); err == nil {
		t.Fatal("ListSubRepos succeeded for empty path")
	}
	if _, err := ListSubRepos(context.Background(), filepath.Join(t.TempDir(), "absent"), SubRepoScanOptions{}); err == nil {
		t.Fatal("ListSubRepos succeeded for missing path")
	}
}

func makeBareGitDir(t *testing.T, dir string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
}

func subRepoRelPaths(result SubRepoListResult) []string {
	paths := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		paths = append(paths, item.RelPath)
	}
	return paths
}

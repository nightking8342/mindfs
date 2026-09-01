package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mindfs/server/internal/fs"
	"mindfs/server/internal/gitview"
)

// A managed root is often a plain container holding several independent
// checkouts. Session worktrees must then target one of those repositories, and
// land inside it rather than under the container.
func TestCreateSessionWorktreeInRepoUsesSubRepositoryParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	parent := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(parent, "registry.json"))
	rootPath := filepath.Join(parent, "workspace")
	nested := filepath.Join(rootPath, "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}
	initWorktreeTestRepo(t, nested)
	dir, err := registry.Upsert(rootPath)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	app := &AppContext{Dirs: registry}
	wt, err := app.CreateSessionWorktreeInRepo(context.Background(), dir.ID, nested, "new", "")
	if err != nil {
		t.Fatalf("CreateSessionWorktreeInRepo: %v", err)
	}

	// The worktree parent must be "<repo>/.worktree", not "<root>/.worktree":
	// generated names are date-sequenced and would collide between siblings.
	wantParent := filepath.Join(nested, ".worktree")
	if got := filepath.Dir(filepath.Clean(wt.Path)); !strings.EqualFold(got, wantParent) {
		t.Fatalf("worktree parent = %q, want %q", got, wantParent)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "app.ts")); err != nil {
		t.Fatalf("worktree lacks sub-repository content: %v", err)
	}
	// It must belong to the sub-repository, and .worktree must be excluded there.
	worktrees, err := gitview.ListWorktrees(context.Background(), nested)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	found := false
	for _, item := range worktrees.Items {
		if strings.EqualFold(filepath.Clean(item.Path), filepath.Clean(wt.Path)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("worktree not registered in the sub-repository: %+v", worktrees.Items)
	}
	excludeData, err := os.ReadFile(filepath.Join(nested, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read sub-repository exclude: %v", err)
	}
	if !strings.Contains(string(excludeData), "/.worktree/") {
		t.Fatalf("sub-repository exclude missing .worktree entry: %q", string(excludeData))
	}
}

// Without the repository check the caller would surface git's raw "not a git
// repository" output from a later step instead of a clear error.
func TestCreateSessionWorktreeInRepoRejectsNonRepository(t *testing.T) {
	parent := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(parent, "registry.json"))
	rootPath := filepath.Join(parent, "workspace")
	plain := filepath.Join(rootPath, "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("MkdirAll plain: %v", err)
	}
	dir, err := registry.Upsert(rootPath)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	app := &AppContext{Dirs: registry}
	_, err = app.CreateSessionWorktreeInRepo(context.Background(), dir.ID, plain, "new", "")
	if err == nil {
		t.Fatal("CreateSessionWorktreeInRepo accepted a directory that is not a repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %v, want a clear not-a-repository message", err)
	}
}

func TestCreateSessionWorktreeInRepoRejectsPathOutsideRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	parent := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(parent, "registry.json"))
	rootPath := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	outside := filepath.Join(parent, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("MkdirAll outside: %v", err)
	}
	initWorktreeTestRepo(t, outside)
	dir, err := registry.Upsert(rootPath)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	app := &AppContext{Dirs: registry}
	if _, err := app.CreateSessionWorktreeInRepo(context.Background(), dir.ID, outside, "new", ""); err == nil {
		t.Fatal("CreateSessionWorktreeInRepo accepted a repository outside the managed root")
	}
}

// The root-scoped entry point must keep behaving exactly as before.
func TestCreateSessionWorktreeKeepsRootBehaviour(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	parent := t.TempDir()
	registry := fs.NewRegistry(filepath.Join(parent, "registry.json"))
	rootPath := filepath.Join(parent, "project")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	initWorktreeTestRepo(t, rootPath)
	dir, err := registry.Upsert(rootPath)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	app := &AppContext{Dirs: registry}
	wt, err := app.CreateSessionWorktree(context.Background(), dir.ID, "new", "")
	if err != nil {
		t.Fatalf("CreateSessionWorktree: %v", err)
	}
	wantParent := filepath.Join(rootPath, ".worktree")
	if got := filepath.Dir(filepath.Clean(wt.Path)); !strings.EqualFold(got, wantParent) {
		t.Fatalf("worktree parent = %q, want %q", got, wantParent)
	}
}

func initWorktreeTestRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
		t.Fatalf("write app.ts: %v", err)
	}
	for _, args := range [][]string{
		{"add", "app.ts"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
}

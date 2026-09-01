package usecase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mindfs/server/internal/agent"
	agenttypes "mindfs/server/internal/agent/types"
	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/gitview"
	"mindfs/server/internal/preferences"
	"mindfs/server/internal/session"
)

func TestListGitSubReposReportsNestedRepos(t *testing.T) {
	rootDir := t.TempDir()
	makeSubRepoFixture(t, filepath.Join(rootDir, "web"))
	makeSubRepoFixture(t, filepath.Join(rootDir, "server"))

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	out, err := svc.ListGitSubRepos(context.Background(), ListGitSubReposInput{RootID: "root"})
	if err != nil {
		t.Fatalf("ListGitSubRepos: %v", err)
	}
	if out.RootIsRepo {
		t.Fatal("RootIsRepo = true, want false")
	}
	got := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		got = append(got, item.RelPath)
	}
	if strings.Join(got, ",") != "server,web" {
		t.Fatalf("rel paths = %v, want [server web]", got)
	}
}

func TestListGitSubReposExcludesRootWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	rootDir := t.TempDir()
	runSubRepoGit(t, rootDir, "init")
	runSubRepoGit(t, rootDir, "config", "user.email", "test@example.com")
	runSubRepoGit(t, rootDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(rootDir, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	runSubRepoGit(t, rootDir, "add", "note.txt")
	runSubRepoGit(t, rootDir, "commit", "-m", "initial")
	// A linked worktree placed where the scan would otherwise find it: not under
	// the dot-prefixed .worktree parent, so only the exclude list keeps it out.
	runSubRepoGit(t, rootDir, "worktree", "add", filepath.Join(rootDir, "wt-feature"), "-b", "feature")
	makeSubRepoFixture(t, filepath.Join(rootDir, "vendored"))

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	out, err := svc.ListGitSubRepos(context.Background(), ListGitSubReposInput{RootID: "root"})
	if err != nil {
		t.Fatalf("ListGitSubRepos: %v", err)
	}
	if !out.RootIsRepo {
		t.Fatal("RootIsRepo = false, want true")
	}
	got := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		got = append(got, item.RelPath)
	}
	if strings.Join(got, ",") != "vendored" {
		t.Fatalf("rel paths = %v, want [vendored]; linked worktree must be excluded", got)
	}
}

func TestResolveRepoPathUnderRoot(t *testing.T) {
	rootDir := t.TempDir()
	nested := filepath.Join(rootDir, "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	resolved, err := resolveRepoPathUnderRoot(rootDir, "")
	if err != nil {
		t.Fatalf("empty repo path: %v", err)
	}
	if resolved != rootDir {
		t.Fatalf("resolved = %q, want %q", resolved, rootDir)
	}

	resolved, err = resolveRepoPathUnderRoot(rootDir, nested)
	if err != nil {
		t.Fatalf("nested repo path: %v", err)
	}
	if !sameManagedDirPath(resolved, nested) {
		t.Fatalf("resolved = %q, want %q", resolved, nested)
	}

	if _, err := resolveRepoPathUnderRoot(rootDir, "web"); err == nil {
		t.Fatal("relative repo path accepted, want error")
	}
	outside := filepath.Join(filepath.Dir(rootDir), "elsewhere")
	if _, err := resolveRepoPathUnderRoot(rootDir, outside); err == nil {
		t.Fatal("repo path outside root accepted, want error")
	}
	escape := filepath.Join(rootDir, "..", "elsewhere")
	if _, err := resolveRepoPathUnderRoot(rootDir, escape); err == nil {
		t.Fatal("traversal repo path accepted, want error")
	}
}

func TestGitReadsScopeToSubRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	rootDir := t.TempDir()
	// The managed root is a plain container: no repository of its own.
	nested := filepath.Join(rootDir, "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	runSubRepoGit(t, nested, "init")
	runSubRepoGit(t, nested, "config", "user.email", "test@example.com")
	runSubRepoGit(t, nested, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(nested, "app.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
		t.Fatalf("write app.ts: %v", err)
	}
	runSubRepoGit(t, nested, "add", "app.ts")
	runSubRepoGit(t, nested, "commit", "-m", "add app")
	if err := os.WriteFile(filepath.Join(nested, "app.ts"), []byte("export const a = 2\n"), 0o644); err != nil {
		t.Fatalf("rewrite app.ts: %v", err)
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	ctx := context.Background()

	status, err := svc.GetGitStatus(ctx, GitStatusInput{RootID: "root", RepoPath: nested})
	if err != nil {
		t.Fatalf("GetGitStatus: %v", err)
	}
	if !status.Status.Available {
		t.Fatal("status unavailable for sub-repository")
	}
	if len(status.Status.Items) != 1 || status.Status.Items[0].Path != "app.ts" {
		t.Fatalf("status items = %+v, want one entry for app.ts", status.Status.Items)
	}

	// Without repo_path the container root is not a repository at all.
	rootStatus, err := svc.GetGitStatus(ctx, GitStatusInput{RootID: "root"})
	if err != nil {
		t.Fatalf("GetGitStatus root: %v", err)
	}
	if rootStatus.Status.Available {
		t.Fatal("container root reported as a repository")
	}

	history, err := svc.GetGitHistory(ctx, GitHistoryInput{RootID: "root", RepoPath: nested, Limit: 10})
	if err != nil {
		t.Fatalf("GetGitHistory: %v", err)
	}
	if !history.History.Available || len(history.History.Items) != 1 {
		t.Fatalf("history = %+v, want one commit", history.History)
	}
	commit := history.History.Items[0].Hash

	branches, err := svc.ListGitBranches(ctx, ListGitBranchesInput{RootID: "root", RepoPath: nested})
	if err != nil {
		t.Fatalf("ListGitBranches: %v", err)
	}
	if branches.Current == "" || len(branches.Branches) == 0 {
		t.Fatalf("branches = %+v, want current branch", branches)
	}

	files, err := svc.GetGitCommitFiles(ctx, GitCommitFilesInput{RootID: "root", RepoPath: nested, Commit: commit})
	if err != nil {
		t.Fatalf("GetGitCommitFiles: %v", err)
	}
	if len(files.Files.Items) != 1 || files.Files.Items[0].Path != "app.ts" {
		t.Fatalf("commit files = %+v, want app.ts", files.Files.Items)
	}

	commitDiff, err := svc.GetGitCommitDiff(ctx, GitCommitDiffInput{
		RootID:   "root",
		RepoPath: nested,
		Commit:   commit,
		Path:     "app.ts",
	})
	if err != nil {
		t.Fatalf("GetGitCommitDiff: %v", err)
	}
	if !strings.Contains(commitDiff.Diff.Content, "export const a = 1") {
		t.Fatalf("commit diff missing content:\n%s", commitDiff.Diff.Content)
	}

	workingDiff, err := svc.GetGitDiff(ctx, GitDiffInput{RootID: "root", RepoPath: nested, Path: "app.ts"})
	if err != nil {
		t.Fatalf("GetGitDiff: %v", err)
	}
	if !strings.Contains(workingDiff.Diff.Content, "+export const a = 2") {
		t.Fatalf("working diff missing change:\n%s", workingDiff.Diff.Content)
	}
}

func TestGitReadsRejectRepoPathOutsideRoot(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "workspace")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	outside := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	ctx := context.Background()

	if _, err := svc.GetGitStatus(ctx, GitStatusInput{RootID: "root", RepoPath: outside}); err == nil {
		t.Fatal("GetGitStatus accepted repo path outside root")
	}
	if _, err := svc.GetGitHistory(ctx, GitHistoryInput{RootID: "root", RepoPath: outside}); err == nil {
		t.Fatal("GetGitHistory accepted repo path outside root")
	}
	if _, err := svc.ListGitBranches(ctx, ListGitBranchesInput{RootID: "root", RepoPath: outside}); err == nil {
		t.Fatal("ListGitBranches accepted repo path outside root")
	}
	if _, err := svc.GetGitCommitFiles(ctx, GitCommitFilesInput{RootID: "root", RepoPath: outside, Commit: "HEAD"}); err == nil {
		t.Fatal("GetGitCommitFiles accepted repo path outside root")
	}
	if _, err := svc.GetGitCommitDiff(ctx, GitCommitDiffInput{
		RootID:   "root",
		RepoPath: outside,
		Commit:   "HEAD",
		Path:     "a.txt",
	}); err == nil {
		t.Fatal("GetGitCommitDiff accepted repo path outside root")
	}
	if _, err := svc.GetGitDiff(ctx, GitDiffInput{RootID: "root", RepoPath: outside, Path: "a.txt"}); err == nil {
		t.Fatal("GetGitDiff accepted repo path outside root")
	}
}

func TestCreateGitWorktreeTargetsSubRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	rootDir := t.TempDir()
	// Container root: not a repository itself.
	nested := filepath.Join(rootDir, "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	runSubRepoGit(t, nested, "init")
	runSubRepoGit(t, nested, "config", "user.email", "test@example.com")
	runSubRepoGit(t, nested, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(nested, "app.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
		t.Fatalf("write app.ts: %v", err)
	}
	runSubRepoGit(t, nested, "add", "app.ts")
	runSubRepoGit(t, nested, "commit", "-m", "initial")

	// The worktree parent lives inside the sub-repository, not the root: generated
	// names are date-sequenced and would collide between sibling repositories.
	parentPath := filepath.Join(nested, ".worktree")
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		t.Fatalf("mkdir worktree parent: %v", err)
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	out, err := svc.CreateGitWorktree(context.Background(), CreateGitWorktreeInput{
		RootID:     "root",
		RepoPath:   nested,
		ParentPath: parentPath,
		Name:       "session-0827-01",
		BranchMode: "new",
	})
	if err != nil {
		t.Fatalf("CreateGitWorktree: %v", err)
	}
	wantPath := filepath.Join(parentPath, "session-0827-01")
	if !sameManagedDirPath(out.Dir.RootPath, wantPath) {
		t.Fatalf("worktree path = %q, want %q", out.Dir.RootPath, wantPath)
	}
	if _, err := os.Stat(filepath.Join(wantPath, "app.ts")); err != nil {
		t.Fatalf("worktree does not contain the sub-repository content: %v", err)
	}
	// The new worktree must belong to the sub-repository.
	worktrees, err := gitview.ListWorktrees(context.Background(), nested)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	found := false
	for _, item := range worktrees.Items {
		if sameManagedDirPath(item.Path, wantPath) {
			found = true
			if item.Branch != "session-0827-01" {
				t.Fatalf("worktree branch = %q, want session-0827-01", item.Branch)
			}
		}
	}
	if !found {
		t.Fatalf("worktree not registered in the sub-repository: %+v", worktrees.Items)
	}
}

func TestCreateGitWorktreeRejectsRepoPathOutsideRoot(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "elsewhere")
	for _, dir := range []string{rootDir, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	_, err := svc.CreateGitWorktree(context.Background(), CreateGitWorktreeInput{
		RootID:     "root",
		RepoPath:   outside,
		ParentPath: filepath.Join(rootDir, ".worktree"),
		Name:       "wt",
		BranchMode: "new",
	})
	if err == nil {
		t.Fatal("CreateGitWorktree accepted a repo path outside the managed root")
	}
}

func TestGitWritesScopeToSubRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	rootDir := t.TempDir()
	// Two sibling repositories under a container root: writes to one must not
	// touch the other.
	first := filepath.Join(rootDir, "web")
	second := filepath.Join(rootDir, "server")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		runSubRepoGit(t, dir, "init")
		runSubRepoGit(t, dir, "config", "user.email", "test@example.com")
		runSubRepoGit(t, dir, "config", "user.name", "Test User")
		if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("export const a = 1\n"), 0o644); err != nil {
			t.Fatalf("write app.ts: %v", err)
		}
		runSubRepoGit(t, dir, "add", "app.ts")
		runSubRepoGit(t, dir, "commit", "-m", "initial")
	}
	// Dirty both repositories identically.
	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("export const a = 2\n"), 0o644); err != nil {
			t.Fatalf("rewrite app.ts: %v", err)
		}
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	ctx := context.Background()

	staged, err := svc.GitStagePath(ctx, GitActionInput{RootID: "root", RepoPath: first, Path: "app.ts"})
	if err != nil {
		t.Fatalf("GitStagePath: %v", err)
	}
	if len(staged.Status.Items) != 1 || !staged.Status.Items[0].Staged {
		t.Fatalf("status after stage = %+v, want app.ts staged", staged.Status.Items)
	}
	// The sibling repository must be untouched.
	secondStatus, err := svc.GetGitStatus(ctx, GitStatusInput{RootID: "root", RepoPath: second})
	if err != nil {
		t.Fatalf("GetGitStatus second: %v", err)
	}
	if len(secondStatus.Status.Items) != 1 || secondStatus.Status.Items[0].Staged {
		t.Fatalf("sibling status = %+v, want app.ts unstaged", secondStatus.Status.Items)
	}

	unstaged, err := svc.GitUnstagePath(ctx, GitActionInput{RootID: "root", RepoPath: first, Path: "app.ts"})
	if err != nil {
		t.Fatalf("GitUnstagePath: %v", err)
	}
	if len(unstaged.Status.Items) != 1 || unstaged.Status.Items[0].Staged {
		t.Fatalf("status after unstage = %+v, want app.ts unstaged", unstaged.Status.Items)
	}

	committed, err := svc.GitCommit(ctx, GitActionInput{RootID: "root", RepoPath: first, Message: "update app"})
	if err != nil {
		t.Fatalf("GitCommit: %v", err)
	}
	if len(committed.Status.Items) != 0 {
		t.Fatalf("status after commit = %+v, want clean", committed.Status.Items)
	}
	// Only the targeted repository advanced; the sibling is still dirty.
	secondStatus, err = svc.GetGitStatus(ctx, GitStatusInput{RootID: "root", RepoPath: second})
	if err != nil {
		t.Fatalf("GetGitStatus second after commit: %v", err)
	}
	if len(secondStatus.Status.Items) != 1 {
		t.Fatalf("sibling status after commit = %+v, want still dirty", secondStatus.Status.Items)
	}
	history, err := svc.GetGitHistory(ctx, GitHistoryInput{RootID: "root", RepoPath: first, Limit: 10})
	if err != nil {
		t.Fatalf("GetGitHistory: %v", err)
	}
	if len(history.History.Items) != 2 || history.History.Items[0].Message != "update app" {
		t.Fatalf("history = %+v, want the new commit on top", history.History.Items)
	}

	// Discard operates on the selected repository only.
	discarded, err := svc.GitDiscardPath(ctx, GitActionInput{
		RootID:   "root",
		RepoPath: second,
		Path:     "app.ts",
		Status:   "M",
	})
	if err != nil {
		t.Fatalf("GitDiscardPath: %v", err)
	}
	if len(discarded.Status.Items) != 0 {
		t.Fatalf("status after discard = %+v, want clean", discarded.Status.Items)
	}

	// Checkout switches branches in the selected repository.
	runSubRepoGit(t, first, "branch", "feature")
	checked, err := svc.CheckoutGitBranch(ctx, CheckoutGitBranchInput{
		RootID:   "root",
		RepoPath: first,
		Branch:   "feature",
	})
	if err != nil {
		t.Fatalf("CheckoutGitBranch: %v", err)
	}
	if checked.Status.Branch != "feature" {
		t.Fatalf("branch after checkout = %q, want feature", checked.Status.Branch)
	}
	branches, err := svc.ListGitBranches(ctx, ListGitBranchesInput{RootID: "root", RepoPath: second})
	if err != nil {
		t.Fatalf("ListGitBranches second: %v", err)
	}
	if branches.Current == "feature" {
		t.Fatal("sibling repository followed the checkout, want it unchanged")
	}
}

// Write operations mutate state, so an unchecked absolute path would let any
// repository on the machine be written to.
func TestGitWritesRejectRepoPathOutsideRoot(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "elsewhere")
	for _, dir := range []string{rootDir, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	ctx := context.Background()
	action := GitActionInput{RootID: "root", RepoPath: outside, Path: "a.txt", Status: "M", Message: "m"}

	if _, err := svc.GitPull(ctx, action); err == nil {
		t.Fatal("GitPull accepted a repo path outside the managed root")
	}
	if _, err := svc.GitPush(ctx, action); err == nil {
		t.Fatal("GitPush accepted a repo path outside the managed root")
	}
	if _, err := svc.GitCommit(ctx, action); err == nil {
		t.Fatal("GitCommit accepted a repo path outside the managed root")
	}
	if _, err := svc.GitStagePath(ctx, action); err == nil {
		t.Fatal("GitStagePath accepted a repo path outside the managed root")
	}
	if _, err := svc.GitUnstagePath(ctx, action); err == nil {
		t.Fatal("GitUnstagePath accepted a repo path outside the managed root")
	}
	if _, err := svc.GitDiscardPath(ctx, action); err == nil {
		t.Fatal("GitDiscardPath accepted a repo path outside the managed root")
	}
	if _, err := svc.CheckoutGitBranch(ctx, CheckoutGitBranchInput{
		RootID:   "root",
		RepoPath: outside,
		Branch:   "main",
	}); err == nil {
		t.Fatal("CheckoutGitBranch accepted a repo path outside the managed root")
	}
}

// Without a repo path every write must still target the managed root, unchanged.
func TestGitWritesDefaultToManagedRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	rootDir := t.TempDir()
	runSubRepoGit(t, rootDir, "init")
	runSubRepoGit(t, rootDir, "config", "user.email", "test@example.com")
	runSubRepoGit(t, rootDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(rootDir, "note.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	runSubRepoGit(t, rootDir, "add", "note.txt")
	runSubRepoGit(t, rootDir, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(rootDir, "note.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("rewrite note: %v", err)
	}

	svc := &Service{Registry: newSubRepoTestRegistry(rootDir)}
	out, err := svc.GitCommit(context.Background(), GitActionInput{RootID: "root", Message: "update note"})
	if err != nil {
		t.Fatalf("GitCommit: %v", err)
	}
	if len(out.Status.Items) != 0 {
		t.Fatalf("status after commit = %+v, want clean", out.Status.Items)
	}
}

func makeSubRepoFixture(t *testing.T, dir string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
}

func runSubRepoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

type subRepoTestRegistry struct {
	root rootfs.RootInfo
}

func newSubRepoTestRegistry(rootPath string) *subRepoTestRegistry {
	return &subRepoTestRegistry{root: rootfs.NewRootInfo("root", "root", rootPath)}
}

func (r *subRepoTestRegistry) GetRoot(rootID string) (rootfs.RootInfo, error) {
	if rootID != r.root.ID {
		return rootfs.RootInfo{}, errors.New("root not found")
	}
	return r.root, nil
}

func (r *subRepoTestRegistry) GetSessionManager(string) (*session.Manager, error) {
	return nil, errors.New("not implemented")
}

func (r *subRepoTestRegistry) UpsertRoot(string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, errors.New("not implemented")
}

func (r *subRepoTestRegistry) RemoveRoot(string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, errors.New("not implemented")
}

func (r *subRepoTestRegistry) RenameRoot(string, string, string) (rootfs.RootInfo, error) {
	return rootfs.RootInfo{}, errors.New("not implemented")
}

func (r *subRepoTestRegistry) ListRoots() []rootfs.RootInfo {
	return []rootfs.RootInfo{r.root}
}

func (r *subRepoTestRegistry) GetAgentPool() *agent.Pool { return nil }

func (r *subRepoTestRegistry) GetPreferences() *preferences.Store { return nil }

func (r *subRepoTestRegistry) GetExternalSessionImporter(string) (agenttypes.ExternalSessionImporter, error) {
	return nil, errors.New("not implemented")
}

func (r *subRepoTestRegistry) GetProber() *agent.Prober { return nil }

func (r *subRepoTestRegistry) GetCandidateRegistry() *CandidateRegistry { return nil }

func (r *subRepoTestRegistry) GetFileWatcher(string, *session.Manager) (*rootfs.SharedFileWatcher, error) {
	return nil, nil
}

func (r *subRepoTestRegistry) ReleaseFileWatcher(string, string) {}

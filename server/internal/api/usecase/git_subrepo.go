package usecase

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"mindfs/server/internal/gitview"
)

// Sub-repository support: a managed root is often a work directory holding
// several independent checkouts rather than a repository itself. These helpers
// discover those repositories and resolve a client-supplied repo path back to a
// concrete repository under the root.

type ListGitSubReposInput struct {
	RootID string
}

type ListGitSubReposOutput struct {
	Items     []gitview.SubRepoItem `json:"items"`
	Truncated bool                  `json:"truncated,omitempty"`
	// RootIsRepo reports whether the managed root is itself a repository, so the
	// client can tell "root repo plus children" from "container of repos".
	RootIsRepo bool `json:"root_is_repo"`
}

func (s *Service) ListGitSubRepos(ctx context.Context, in ListGitSubReposInput) (ListGitSubReposOutput, error) {
	if err := s.ensureRegistry(); err != nil {
		return ListGitSubReposOutput{}, err
	}
	root, err := s.Registry.GetRoot(in.RootID)
	if err != nil {
		return ListGitSubReposOutput{}, err
	}
	rootIsRepo := false
	if ok, err := gitview.HasRepo(ctx, root.RootPath); err == nil {
		rootIsRepo = ok
	}

	// Linked worktrees of the root repository are already listed by the worktrees
	// view; reporting them here as well would duplicate every task worktree.
	excluded := make([]string, 0, 4)
	if rootIsRepo {
		if worktrees, err := gitview.ListWorktrees(ctx, root.RootPath); err == nil {
			for _, item := range worktrees.Items {
				if strings.TrimSpace(item.Path) != "" {
					excluded = append(excluded, item.Path)
				}
			}
		}
	}

	result, err := gitview.ListSubRepos(ctx, root.RootPath, gitview.SubRepoScanOptions{
		ExcludePaths: excluded,
	})
	if err != nil {
		return ListGitSubReposOutput{}, err
	}
	return ListGitSubReposOutput{
		Items:      result.Items,
		Truncated:  result.Truncated,
		RootIsRepo: rootIsRepo,
	}, nil
}

// ResolveRepoPath exposes repository path validation to callers outside this
// package (the app context, when creating worktrees in a sub-repository).
func (s *Service) ResolveRepoPath(rootPath, repoPath string) (string, error) {
	return resolveRepoPathUnderRoot(rootPath, repoPath)
}

// resolveRepoPathUnderRoot validates a client-supplied absolute repository path
// and returns the path to operate on. An empty repoPath means the managed root
// itself.
//
// The path must stay inside the managed root: read endpoints are already behind
// authentication, but keeping every repo-scoped call inside the root means the
// same helper can back write operations later without widening their reach.
func resolveRepoPathUnderRoot(rootPath, repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return rootPath, nil
	}
	if !filepath.IsAbs(repoPath) {
		return "", errors.New("repo path must be absolute")
	}
	cleaned := filepath.Clean(repoPath)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = filepath.Clean(resolved)
	}
	rootClean := filepath.Clean(strings.TrimSpace(rootPath))
	if resolved, err := filepath.EvalSymlinks(rootClean); err == nil {
		rootClean = filepath.Clean(resolved)
	}
	if sameManagedDirPath(cleaned, rootClean) {
		return rootClean, nil
	}
	if !repoPathInsideRoot(cleaned, rootClean) {
		return "", errors.New("repo path must be inside the managed root")
	}
	return cleaned, nil
}

func repoPathInsideRoot(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		// filepath.Rel compares case-sensitively, but Windows paths are not; a
		// drive letter or directory differing only in case would be rejected.
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

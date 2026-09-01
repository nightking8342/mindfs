import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const actionBar = readFileSync(new URL("../src/components/ActionBar.tsx", import.meta.url), "utf8");
const selector = readFileSync(new URL("../src/components/WorktreeRepoSelector.tsx", import.meta.url), "utf8");
const sessionService = readFileSync(new URL("../src/services/session.ts", import.meta.url), "utf8");
const zhCN = readFileSync(new URL("../src/i18n/locales/zh-CN.ts", import.meta.url), "utf8");
const enUS = readFileSync(new URL("../src/i18n/locales/en-US.ts", import.meta.url), "utf8");

// The toggle used to hang off is_git_repo, so a container root holding several
// checkouts showed no worktree button at all.
assert.match(
  actionBar,
  /const canCreateWorktree = worktreeRepoOptions\.length > 0/,
  "worktree availability should follow the candidate repositories, not the root's own repo flag",
);
assert.match(
  actionBar,
  /\{!currentSession && canCreateWorktree \?/,
  "the worktree toggle should render whenever some repository can host a worktree",
);
assert.match(
  actionBar,
  /planModeActive \|\| \(!currentSession && canCreateWorktree && mode !== "command"\)/,
  "the toggle row should be shown for container roots too",
);

// Candidates = the root itself when it is a repository, plus discovered repos.
assert.match(
  actionBar,
  /const worktreeRepoOptions = useMemo<WorktreeRepoOption\[\]>\(\(\) => \{[\s\S]*?if \(currentRootIsGitRepo\) \{[\s\S]*?options\.push\(\{ path: "",[\s\S]*?currentRootSubRepos \|\| \[\]/,
  "candidates should combine the root repository with the discovered sub-repositories",
);

// Progressive disclosure: a single candidate must look exactly like before.
assert.match(
  actionBar,
  /const showWorktreeRepoSelector = worktreeRepoOptions\.length > 1/,
  "the repository picker should only appear when there is a real choice",
);
assert.match(
  actionBar,
  /const worktreeRepoNeedsChoice = showWorktreeRepoSelector && !currentRootIsGitRepo && !worktreeRepoChosen/,
  "a container root with several repositories should require an explicit choice",
);

// Defaults: root when it is a repository, otherwise a lone sub-repository.
assert.match(
  actionBar,
  /if \(currentRootIsGitRepo \|\| worktreeRepoChosen\) \{\s*return;\s*\}[\s\S]*?if \(subRepos\.length === 1\) \{[\s\S]*?setWorktreeRepoPath\(subRepos\[0\]\.path\)/,
  "a single sub-repository should be preselected when the root is not a repository",
);

// A pending choice must not silently fall back to the root, which would create
// the worktree in the wrong place.
assert.match(
  actionBar,
  /create: createWorktree && worktreeRepoReady/,
  "sending with an unresolved repository choice must not create a worktree",
);
assert.match(
  actionBar,
  /repoPath: worktreeRepoPath,/,
  "the selected repository should be sent with the message",
);
assert.match(
  actionBar,
  /\{createWorktree && worktreeRepoReady \? <><WorktreeBranchSelector/,
  "the branch selector should wait until a repository is settled",
);

// Branches are per repository; a stale list would be sent as an existing branch.
assert.match(
  actionBar,
  /fetchGitBranches\(currentRootId, \{ repoPath: worktreeRepoPath \|\| undefined \}\)/,
  "branches should be fetched for the selected repository",
);
assert.match(
  actionBar,
  /useEffect\(\(\) => \{\s*setWorktreeBranchMode\("new"\);\s*setWorktreeBranch\(""\);\s*setWorktreeBranches\(\{ branches: \[\] \}\);\s*\}, \[worktreeRepoPath\]\)/,
  "switching repositories should reset the branch selection",
);
// A repository can disappear between renders (removed, root rescanned).
assert.match(
  actionBar,
  /if \(!worktreeRepoOptions\.some\(\(item\) => item\.path === worktreeRepoPath\)\) \{[\s\S]*?setWorktreeRepoPath\(""\)/,
  "a selection that no longer exists should be dropped",
);

// The picker mirrors the branch selector and self-opens when nothing is chosen.
assert.match(
  selector,
  /autoOpenedRef\.current = true;\s*setOpen\(true\)/,
  "the picker should open itself once when no repository is preselected",
);
assert.match(
  selector,
  /useViewportMenu\(\{[\s\S]*?menuPlacement,/,
  "the picker should delegate placement to useViewportMenu like the branch selector",
);
assert.match(
  selector,
  /document\.addEventListener\("pointerdown", handlePointerOutside\)[\s\S]*?document\.addEventListener\("keydown", handleKeyDown\)/,
  "the picker should dismiss on outside pointer and Escape",
);

// The wire payload and the discovery trigger.
assert.match(
  sessionService,
  /worktree_repo_path: newSessionWorktree\?\.repoPath \|\| ""/,
  "the session message should carry the target repository",
);
assert.match(
  app,
  /currentRootSubRepos=\{actionBarWorktreeRepos\}/,
  "the composer should receive the discovered repositories",
);
assert.match(
  app,
  /const actionBarWorktreeRepos = useMemo\([\s\S]*?gitSubReposByRoot\[currentRootId \|\| ""\]\?\.items/,
  "composer candidates should reuse the git tab's discovery cache",
);
assert.match(
  app,
  /const needsSubRepoDiscoveryForComposer = !selectedSession && !currentSession[\s\S]*?projectTreeTab !== "git" && !needsSubRepoDiscoveryForComposer[\s\S]*?refreshGitSubRepos\(currentRootId\)/,
  "discovery should also run for the composer's new-session state, still cached per root",
);

for (const [locale, name] of [[zhCN, "zh-CN"], [enUS, "en-US"]]) {
  assert.ok(
    locale.includes('"worktree.selectRepository"'),
    `${name} should define worktree.selectRepository`,
  );
}

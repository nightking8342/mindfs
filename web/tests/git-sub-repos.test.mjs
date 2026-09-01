import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const gitService = readFileSync(new URL("../src/services/git.ts", import.meta.url), "utf8");
const zhCN = readFileSync(new URL("../src/i18n/locales/zh-CN.ts", import.meta.url), "utf8");
const enUS = readFileSync(new URL("../src/i18n/locales/en-US.ts", import.meta.url), "utf8");

// Repo-scoped reads must all thread repo_path, otherwise a sub-repository panel
// silently shows the managed root's data instead.
for (const [fn, name] of [
  ["fetchGitStatus", "status"],
  ["fetchGitHistory", "history"],
  ["fetchGitBranches", "branches"],
  ["fetchGitCommitFiles", "commit files"],
  ["fetchGitCommitDiff", "commit diff"],
]) {
  // Params may be built before or inline with the appURL call, so match the
  // function body rather than a fixed ordering.
  const body = gitService.slice(gitService.indexOf(`export async function ${fn}`));
  const end = body.indexOf("\nexport ", 1);
  const scoped = end < 0 ? body : body.slice(0, end);
  assert.ok(
    scoped.includes("withRepoPath"),
    `${name} request should pass repo_path through withRepoPath`,
  );
  assert.match(
    scoped,
    /options\?: GitRepoScope|\} & GitRepoScope/,
    `${name} should accept a repository scope`,
  );
}

assert.match(
  gitService,
  /function withRepoPath[\s\S]*?if \(value\) \{[\s\S]*?params\.set\("repo_path", value\)/,
  "withRepoPath should omit repo_path when the scope is the managed root",
);

// Caches key on root plus repository. Keying on root alone would serve one
// sub-repository's history for another.
assert.match(
  gitService,
  /export function gitScopeKey\(rootId: string, repoPath\?: string\): string \{[\s\S]*?return repo \? `\$\{rootId\}\$\{GIT_SCOPE_SEP\}\$\{repo\}` : rootId/,
  "scope key should fall back to the bare rootId so existing cache entries stay valid",
);
for (const [call, name] of [
  ["getHistoryCacheEntry\\(scope\\)", "history read"],
  ["setHistoryCacheEntry\\(scope,", "history write"],
  ["commitFilesStorageKey\\(scope,", "commit files storage"],
  ["commitDiffStorageKey\\(scope,", "commit diff storage"],
]) {
  assert.match(gitService, new RegExp(call), `${name} should be scoped per repository`);
}
assert.match(
  gitService,
  /const key = `\$\{scope\}:\$\{beforeCommit\}/,
  "history inflight key should include the repository scope",
);

// A root-wide purge has to reach every sub-repository, since callers only know
// the rootId.
assert.match(
  gitService,
  /export function clearGitHistoryCache\(rootId\?: string, options\?: GitRepoScope\)[\s\S]*?const scopePrefixes = repoPath[\s\S]*?\[`\$\{rootId\}:`, `\$\{rootId\}\$\{GIT_SCOPE_SEP\}`\]/,
  "clearing by root alone should also drop every sub-repository scope",
);

assert.match(
  gitService,
  /export async function fetchGitSubRepos[\s\S]*?appURL\("\/api\/git\/repos"/,
  "sub-repository discovery should call /api/git/repos",
);
assert.match(
  gitService,
  /root_is_repo: payload\?\.root_is_repo === true/,
  "discovery payload should report whether the managed root is itself a repository",
);

// The scan touches the filesystem, so it must be gated on somewhere actually
// needing the result (git tab or the composer) and cached per root afterwards.
assert.match(
  app,
  /if \(projectTreeTab !== "git" && !needsSubRepoDiscoveryForComposer\) \{\s*return;\s*\}[\s\S]*?gitSubReposByRoot\[currentRootId\] \|\| gitSubReposLoadingByRoot\[currentRootId\][\s\S]*?refreshGitSubRepos\(currentRootId\)/,
  "discovery should run once per root and only where its result is used",
);
assert.match(
  app,
  /case "git": \{[\s\S]*?refreshGitSubRepos\(root\)[\s\S]*?gitScopeExpanded\[gitScopeKey\(root, item\.path\)\] === true/,
  "manual refresh should rediscover repositories but only reload expanded ones",
);

// A container root holding repositories must not report "not a git repository".
assert.match(
  app,
  /if \(!isRootRepo\) \{[\s\S]*?if \(subRepoItems\.length > 0\) \{[\s\S]*?return subRepoSection/,
  "a non-repository root should still render its discovered repositories",
);
assert.match(
  app,
  /return \(\s*<div style=\{\{ padding: "8px 4px", fontSize: "12px", color: "var\(--text-secondary\)" \}\}>\s*\{t\("git\.notRepository"\)\}/,
  "the not-a-repository message should remain for roots with no repositories at all",
);

// Sub-repository panels must open diffs against their own repository.
assert.match(
  app,
  /openGitDiff\(root, statusItem, \{ repoPath: item\.path \}\)/,
  "sub-repository changes should open a diff scoped to that repository",
);
assert.match(
  app,
  /openGitCommitDiff\(root, commit, statusItem, \{ repoPath: item\.path \}\)/,
  "sub-repository history should open commit diffs scoped to that repository",
);
assert.match(
  app,
  /const openGitCommitDiff = useCallback\([\s\S]*?options\?: \{ repoPath\?: string \}[\s\S]*?fetchGitCommitDiff\(rootID, commit\.hash, item, \{\s*repoPath: options\?\.repoPath,/,
  "openGitCommitDiff should forward repoPath to the service layer",
);

// Sub-repository state must stay separate from the root's, whose loaders bail
// out early when the root is not a repository.
for (const state of [
  "gitStatusByScope",
  "gitHistoryByScope",
  "gitStatusLoadingByScope",
  "gitHistoryLoadingByScope",
  "gitScopeExpanded",
]) {
  assert.ok(app.includes(state), `${state} should hold per-repository git state`);
}
assert.match(
  app,
  /const refreshGitScopeStatus = useCallback\(async \(rootID: string, repoPath: string\)[\s\S]*?fetchGitStatus\(rootID, \{ repoPath \}\)/,
  "scoped status refresh should request the sub-repository explicitly",
);
assert.match(
  app,
  /const refreshGitScopeHistory = useCallback\([\s\S]*?getCachedGitHistoryHead\(rootID, undefined, \{ repoPath \}\)/,
  "scoped history refresh should read the cache for that repository",
);

// Expanding a repository is what triggers its first load; without this the
// section stays permanently empty.
assert.match(
  app,
  /if \(!expanded && !status && !statusLoading\) \{[\s\S]*?refreshGitScopeStatus\(root, item\.path\)[\s\S]*?refreshGitScopeHistory\(root, item\.path\)/,
  "expanding a repository should lazily load its status and history",
);

for (const [locale, name] of [[zhCN, "zh-CN"], [enUS, "en-US"]]) {
  for (const key of [
    "git.subRepositories",
    "git.subRepositoriesTruncated",
    "git.scanningSubRepositories",
  ]) {
    assert.ok(locale.includes(`"${key}"`), `${name} should define ${key}`);
  }
}

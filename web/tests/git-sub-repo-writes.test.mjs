import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const gitService = readFileSync(new URL("../src/services/git.ts", import.meta.url), "utf8");
const statusPanel = readFileSync(new URL("../src/components/GitStatusPanel.tsx", import.meta.url), "utf8");
const zhCN = readFileSync(new URL("../src/i18n/locales/zh-CN.ts", import.meta.url), "utf8");
const enUS = readFileSync(new URL("../src/i18n/locales/en-US.ts", import.meta.url), "utf8");

// Every write must carry the target repository, and omit it for the root so the
// existing root-only behaviour is preserved byte for byte.
assert.match(
  gitService,
  /function gitActionBody\(rootId: string, repoPath: string \| undefined, extra\?: Record<string, unknown>\): string \{[\s\S]*?\.\.\.\(repo \? \{ repo_path: repo \} : \{\}\)/,
  "the write body helper should include repo_path only when a repository is given",
);
for (const fn of [
  "pullGit",
  "pushGit",
  "commitGit",
  "stageGitItem",
  "unstageGitItem",
  "discardGitItem",
  "checkoutGitBranch",
]) {
  const body = gitService.slice(gitService.indexOf(`export async function ${fn}`));
  const end = body.indexOf("\nexport ", 1);
  const scoped = end < 0 ? body : body.slice(0, end);
  assert.ok(scoped.includes("options?: GitRepoScope"), `${fn} should accept a repository scope`);
  assert.ok(
    scoped.includes("gitActionBody(rootId, options?.repoPath"),
    `${fn} should send the selected repository`,
  );
}

// Post-action refresh must land in the sub-repository's own state: writing its
// status into the by-root state would relabel the root panel.
assert.match(
  app,
  /if \(repoPath\) \{\s*const scope = gitScopeKey\(rootID, repoPath\);\s*clearGitHistoryCache\(rootID, \{ repoPath \}\);\s*setGitStatusByScope/,
  "a sub-repository write should refresh that repository's scoped state",
);
assert.match(
  app,
  /refreshGitScopeHistory\(rootID, repoPath, \{ force: true \}\)/,
  "a sub-repository write should force-refresh that repository's history",
);
// The root path must keep its original behaviour.
assert.match(
  app,
  /clearGitHistoryCache\(rootID\);\s*setGitStatusByRoot\(\(prev\) => \(\{ \.\.\.prev, \[rootID\]: nextStatus \}\)\);\s*if \(options\?\.refreshHistory !== false\) \{\s*await refreshGitHistory\(rootID, \{ force: true \}\)/,
  "a root-level write should still refresh the by-root state",
);

// Each handler threads repoPath through to both the request and the refresh.
for (const [handler, action] of [
  ["handleGitPull", "pull"],
  ["handleGitPush", "push"],
  ["handleGitCommit", "commit"],
  ["handleGitStageItem", "stage"],
  ["handleGitUnstageItem", "unstage"],
  ["handleGitDiscardItem", "discard"],
]) {
  const start = app.indexOf(`const ${handler} = useCallback(`);
  assert.ok(start > 0, `${handler} should exist`);
  const scoped = app.slice(start, start + 520);
  assert.ok(scoped.includes("repoPath?: string"), `${handler} should accept an optional repository`);
  assert.ok(scoped.includes("{ repoPath }"), `${handler} should forward the repository to ${action}`);
}

assert.match(
  app,
  /const switchGitBranch = useCallback\(\s*async \(rootID: string, branch: string, repoPath\?: string\)[\s\S]*?checkoutGitBranch\(rootID, branch, \{ repoPath: targetRepoPath \}\)/,
  "branch switching should target the selected repository",
);

// The branch menu loads branches for the repository it is showing; without this
// a sub-repository panel would list the root's branches.
assert.match(
  statusPanel,
  /fetchGitBranches\(rootId, \{ repoPath \}\)/,
  "the status panel should load branches for the repository it displays",
);
assert.match(
  statusPanel,
  /\}, \[branchMenuOpen, repoPath, rootId\]\)/,
  "changing the displayed repository should reload its branches",
);

// The sub-repository panel had writes explicitly disabled; C tier enables them.
const sectionStart = app.indexOf("const renderSubRepoGitSection");
assert.ok(sectionStart > 0, "renderSubRepoGitSection should exist");
const section = app.slice(sectionStart, app.indexOf("const renderRootGitContent", sectionStart));
assert.ok(
  !section.includes("showHeaderActions={false}"),
  "the sub-repository panel should no longer hide its write actions",
);
assert.ok(
  !section.includes("enableBranchMenu={false}"),
  "the sub-repository panel should no longer disable its branch menu",
);
assert.match(section, /repoPath=\{item\.path\}/, "the panel should know which repository it shows");
for (const prop of ["onDiscardItem", "onStageItem", "onPull", "onPush", "onCommit", "onSwitchBranch"]) {
  assert.ok(section.includes(prop), `the sub-repository panel should wire ${prop}`);
}
assert.match(
  section,
  /handleGitDiscardItem\(root, statusItem, item\.path\)/,
  "discard should be scoped to the sub-repository",
);
assert.match(
  section,
  /statusItem\.staged === true\s*\? handleGitUnstageItem\(root, statusItem, item\.path\)\s*: handleGitStageItem\(root, statusItem, item\.path\)/,
  "stage and unstage should be scoped to the sub-repository",
);

// "commit -am" covers every tracked change, and several commit buttons now sit
// side by side, so the target repository is confirmed by name.
assert.match(
  section,
  /window\.confirm\(\s*t\("git\.confirmCommitRepository", \{ repo: item\.rel_path \|\| item\.name \}\),\s*\)[\s\S]*?handleGitCommit\(root, message, item\.path\)/,
  "committing a sub-repository should confirm which repository is affected",
);

// Status paths are relative to the sub-repository, not the managed root.
assert.match(
  section,
  /path: joinRootRelativePath\(item\.rel_path, statusItem\.path\)/,
  "opening a sub-repository file should resolve against that repository",
);
assert.match(
  app,
  /function joinRootRelativePath\(prefix: string, path: string\): string \{[\s\S]*?return `\$\{normalizedPrefix\}\/\$\{normalizedPath\}`/,
  "path joining should produce a root-relative path",
);

for (const [locale, name] of [[zhCN, "zh-CN"], [enUS, "en-US"]]) {
  assert.ok(
    locale.includes('"git.confirmCommitRepository"'),
    `${name} should define git.confirmCommitRepository`,
  );
}

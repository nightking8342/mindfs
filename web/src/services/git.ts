import { appURL } from "./base";
import { protectedJSON } from "./api";
import { getCachedGitDiff, setCachedGitDiff, type CachedGitDiffPayload } from "./file";

export type GitStatusCode = "M" | "A" | "D" | "R" | "??";

export type GitStatusItem = {
  path: string;
  display_path?: string;
  old_path?: string;
  status: GitStatusCode;
  staged?: boolean;
  additions: number;
  deletions: number;
  is_dir?: boolean;
};

export type GitStatusPayload = {
  available: boolean;
  branch?: string;
  dirty_count: number;
  items: GitStatusItem[];
};

export type GitDiffPayload = CachedGitDiffPayload & {
  path: string;
  display_path?: string;
  old_path?: string;
  status: GitStatusCode | string;
  additions: number;
  deletions: number;
  content: string;
  commit?: string;
  base_head?: string;
  target_head?: string;
  source?: "worktree" | "commit" | "commit_range";
};

export type GitHistoryItem = {
  hash: string;
  message: string;
  commit_time: string;
  remote?: boolean;
};

export type GitHistoryPayload = {
  available: boolean;
  items: GitHistoryItem[];
  has_more: boolean;
  commit_missing?: boolean;
  remote_head?: string;
};

export type GitCommitFilesPayload = {
  commit: string;
  items: GitStatusItem[];
};

export type GitBranchItem = {
  name: string;
  current: boolean;
};

export type GitBranchesPayload = {
  current?: string;
  branches: GitBranchItem[];
};

export type GitWorktreeItem = {
  path: string;
  branch?: string;
  head?: string;
  current: boolean;
};

export type GitWorktreesPayload = {
  items: GitWorktreeItem[];
};

/** An independent git repository nested under a managed root. */
export type GitSubRepoItem = {
  path: string;
  rel_path: string;
  name: string;
  branch?: string;
  head?: string;
};

export type GitSubReposPayload = {
  items: GitSubRepoItem[];
  truncated: boolean;
  /** Whether the managed root is itself a repository. */
  root_is_repo: boolean;
};

/**
 * Scope for repo-aware git calls. An empty repoPath means the managed root
 * itself, so callers can pass the value through without branching.
 */
export type GitRepoScope = { repoPath?: string };

function withRepoPath(params: URLSearchParams, repoPath?: string): URLSearchParams {
  const value = String(repoPath || "").trim();
  if (value) {
    params.set("repo_path", value);
  }
  return params;
}

export type GitActionPayload = {
  output: string;
  status: GitStatusPayload;
};

function normalizeGitStatusPayload(payload: any): GitStatusPayload {
  return {
    available: payload?.available === true,
    branch: typeof payload?.branch === "string" ? payload.branch : undefined,
    dirty_count: Number(payload?.dirty_count) || 0,
    items: Array.isArray(payload?.items) ? payload.items as GitStatusItem[] : [],
  };
}

function normalizeGitActionPayload(payload: any): GitActionPayload {
  return {
    output: typeof payload?.output === "string" ? payload.output : "",
    status: normalizeGitStatusPayload(payload?.status || {}),
  };
}

export async function fetchGitSubRepos(rootId: string): Promise<GitSubReposPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/repos", new URLSearchParams({ root: rootId })));
  return {
    items: Array.isArray(payload?.items)
      ? payload.items
          .map((item: any) => ({
            path: typeof item?.path === "string" ? item.path : "",
            rel_path: typeof item?.rel_path === "string" ? item.rel_path : "",
            name: typeof item?.name === "string" ? item.name : "",
            branch: typeof item?.branch === "string" ? item.branch : undefined,
            head: typeof item?.head === "string" ? item.head : undefined,
          }))
          .filter((item: GitSubRepoItem) => !!item.path)
      : [],
    truncated: payload?.truncated === true,
    root_is_repo: payload?.root_is_repo === true,
  };
}

export async function fetchGitStatus(rootId: string, options?: GitRepoScope): Promise<GitStatusPayload> {
  const params = withRepoPath(new URLSearchParams({ root: rootId }), options?.repoPath);
  const payload = await protectedJSON<any>(appURL("/api/git/status", params));
  return normalizeGitStatusPayload(payload);
}

export async function fetchGitStatusByPath(path: string): Promise<GitStatusPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/status", new URLSearchParams({ path })));
  return normalizeGitStatusPayload(payload);
}

const DEFAULT_HISTORY_LIMIT = 10;
const HISTORY_LIST_STORAGE_PREFIX = "mindfs.git.history.list:";
const COMMIT_FILES_STORAGE_PREFIX = "mindfs.git.history.files:";
const COMMIT_DIFF_STORAGE_PREFIX = "mindfs.git.history.diff.v2:";

type GitHistoryCacheEntry = {
  items: GitHistoryItem[];
  hasMore: boolean;
  remoteHead?: string;
};

const gitHistoryListCache = new Map<string, GitHistoryCacheEntry>();
const gitHistoryInflight = new Map<string, Promise<GitHistoryPayload>>();
const gitCommitFilesCache = new Map<string, GitCommitFilesPayload>();
const gitCommitFilesInflight = new Map<string, Promise<GitCommitFilesPayload>>();
const gitCommitDiffCache = new Map<string, GitDiffPayload>();
const gitCommitDiffInflight = new Map<string, Promise<GitDiffPayload>>();

function canUseStorage(): boolean {
  return typeof window !== "undefined" && !!window.localStorage;
}

function readStorageJSON<T>(key: string): T | null {
  if (!canUseStorage()) return null;
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? JSON.parse(raw) as T : null;
  } catch {
    return null;
  }
}

function writeStorageJSON(key: string, value: unknown): void {
  if (!canUseStorage()) return;
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {}
}

function removeStorageByPrefix(prefix: string): void {
  if (!canUseStorage()) return;
  for (const key of Array.from({ length: window.localStorage.length }, (_, index) => window.localStorage.key(index)).filter(Boolean) as string[]) {
    if (key.startsWith(prefix)) {
      window.localStorage.removeItem(key);
    }
  }
}

// Separator for the "<rootId>@@<repoPath>" scope segment of a cache key. A root
// id is a directory basename and may contain spaces, so a printable sequence
// that does not occur in a path is used rather than a single character.
const GIT_SCOPE_SEP = "@@";

/**
 * Cache scope for history and commit data. A managed root can hold several
 * independent repositories, so caches key on root plus repository instead of
 * root alone.
 *
 * With no repoPath the scope is the bare rootId, keeping already persisted
 * localStorage keys valid across the upgrade.
 */
export function gitScopeKey(rootId: string, repoPath?: string): string {
  const repo = String(repoPath || "").trim();
  return repo ? `${rootId}${GIT_SCOPE_SEP}${repo}` : rootId;
}

function historyListStorageKey(scope: string): string {
  return `${HISTORY_LIST_STORAGE_PREFIX}${encodeURIComponent(scope)}`;
}

function commitFilesStorageKey(scope: string, commit: string): string {
  return `${COMMIT_FILES_STORAGE_PREFIX}${encodeURIComponent(scope)}:${encodeURIComponent(commit)}`;
}

function commitDiffStorageKey(scope: string, commit: string, oldPath: string, path: string): string {
  return `${COMMIT_DIFF_STORAGE_PREFIX}${encodeURIComponent(scope)}:${encodeURIComponent(commit)}:${encodeURIComponent(oldPath)}:${encodeURIComponent(path)}`;
}

function getHistoryCacheEntry(scope: string): GitHistoryCacheEntry | null {
  const cached = gitHistoryListCache.get(scope);
  if (cached) {
    return cached;
  }
  const persisted = readStorageJSON<GitHistoryCacheEntry>(historyListStorageKey(scope));
  if (persisted && Array.isArray(persisted.items)) {
    const normalized = {
      items: persisted.items.filter((item) => !!item?.hash),
      hasMore: persisted.hasMore === true,
      remoteHead: typeof persisted.remoteHead === "string" ? persisted.remoteHead : undefined,
    };
    normalized.items = applyRemoteHead(normalized.items, normalized.remoteHead);
    gitHistoryListCache.set(scope, normalized);
    return normalized;
  }
  return null;
}

function setHistoryCacheEntry(scope: string, entry: GitHistoryCacheEntry): void {
  gitHistoryListCache.set(scope, entry);
  writeStorageJSON(historyListStorageKey(scope), entry);
}

function normalizeGitHistoryPayload(payload: any): GitHistoryPayload {
  return {
    available: payload?.available === true,
    items: Array.isArray(payload?.items)
      ? payload.items
          .map((item: any) => ({
            hash: typeof item?.hash === "string" ? item.hash : "",
            message: typeof item?.message === "string" ? item.message : "",
            commit_time: typeof item?.commit_time === "string" ? item.commit_time : "",
            remote: item?.remote === true,
          }))
          .filter((item: GitHistoryItem) => !!item.hash)
      : [],
    has_more: payload?.has_more === true,
    commit_missing: payload?.commit_missing === true,
    remote_head: typeof payload?.remote_head === "string" ? payload.remote_head : undefined,
  };
}

function applyRemoteHead(items: GitHistoryItem[], remoteHead?: string): GitHistoryItem[] {
  if (!remoteHead) {
    return items;
  }
  const remoteHeadIndex = items.findIndex((item) => item.hash === remoteHead);
  if (remoteHeadIndex < 0) {
    return items;
  }
  return items.map((item, index) => (
    index >= remoteHeadIndex ? { ...item, remote: true } : item
  ));
}

function mergeHistoryItems(existing: GitHistoryItem[], next: GitHistoryItem[]): GitHistoryItem[] {
  const seen = new Set(existing.map((item) => item.hash));
  const merged = existing.slice();
  next.forEach((item) => {
    if (!seen.has(item.hash)) {
      seen.add(item.hash);
      merged.push(item);
    }
  });
  return merged;
}

export function getCachedGitHistory(rootId: string, options?: GitRepoScope): GitHistoryPayload | null {
  const cached = getHistoryCacheEntry(gitScopeKey(rootId, options?.repoPath));
  if (!cached) {
    return null;
  }
  return {
    available: true,
    items: cached.items.slice(),
    has_more: cached.hasMore,
    remote_head: cached.remoteHead,
  };
}

export function getCachedGitHistoryHead(
  rootId: string,
  limit = DEFAULT_HISTORY_LIMIT,
  options?: GitRepoScope,
): GitHistoryPayload | null {
  const cached = getHistoryCacheEntry(gitScopeKey(rootId, options?.repoPath));
  if (!cached) {
    return null;
  }
  return {
    available: true,
    items: cached.items.slice(0, limit),
    has_more: cached.items.length > limit || cached.hasMore,
    remote_head: cached.remoteHead,
  };
}

/**
 * Drops cached history and commit data.
 *
 * Passing only a rootId clears every repository under that root; add repoPath to
 * limit the purge to one sub-repository.
 */
export function clearGitHistoryCache(rootId?: string, options?: GitRepoScope): void {
  const repoPath = String(options?.repoPath || "").trim();
  const scope = rootId ? gitScopeKey(rootId, repoPath) : "";
  // Match by prefix instead of splitting: a scope embeds a repository path, which
  // can itself contain the ":" that separates a scope from the rest of the key.
  // A root-wide purge covers the root's own keys ("<rootId>:...") plus every
  // sub-repository ("<rootId>@@<path>...").
  const scopePrefixes = repoPath
    ? [`${scope}:`]
    : [`${rootId}:`, `${rootId}${GIT_SCOPE_SEP}`];
  const matchesScopePrefix = (candidate: string): boolean =>
    scopePrefixes.some((prefix) => candidate.startsWith(prefix));

  if (rootId) {
    for (const key of Array.from(gitHistoryListCache.keys())) {
      // The history cache keys on the scope alone, with no trailing segment.
      if (key === scope || (!repoPath && key.startsWith(`${rootId}${GIT_SCOPE_SEP}`))) {
        gitHistoryListCache.delete(key);
      }
    }
    if (canUseStorage()) {
      window.localStorage.removeItem(historyListStorageKey(scope));
      if (!repoPath) {
        removeStorageByPrefix(`${HISTORY_LIST_STORAGE_PREFIX}${encodeURIComponent(`${rootId}${GIT_SCOPE_SEP}`)}`);
      }
    }
    for (const prefix of [COMMIT_FILES_STORAGE_PREFIX, COMMIT_DIFF_STORAGE_PREFIX]) {
      removeStorageByPrefix(`${prefix}${encodeURIComponent(scope)}:`);
      if (!repoPath) {
        removeStorageByPrefix(`${prefix}${encodeURIComponent(`${rootId}${GIT_SCOPE_SEP}`)}`);
      }
    }
  } else {
    gitHistoryListCache.clear();
    removeStorageByPrefix(HISTORY_LIST_STORAGE_PREFIX);
    removeStorageByPrefix(COMMIT_FILES_STORAGE_PREFIX);
    removeStorageByPrefix(COMMIT_DIFF_STORAGE_PREFIX);
  }
  const clearMap = (cache: Map<string, unknown>) => {
    for (const key of Array.from(cache.keys())) {
      if (!rootId || matchesScopePrefix(key)) {
        cache.delete(key);
      }
    }
  };
  clearMap(gitHistoryInflight as Map<string, unknown>);
  clearMap(gitCommitFilesCache as Map<string, unknown>);
  clearMap(gitCommitFilesInflight as Map<string, unknown>);
  clearMap(gitCommitDiffCache as Map<string, unknown>);
  clearMap(gitCommitDiffInflight as Map<string, unknown>);
}

export async function fetchGitHistory(
  rootId: string,
  options?: {
    beforeCommit?: string;
    afterCommit?: string;
    limit?: number;
    force?: boolean;
  } & GitRepoScope,
): Promise<GitHistoryPayload> {
  const limit = options?.limit || DEFAULT_HISTORY_LIMIT;
  const beforeCommit = options?.beforeCommit || "";
  const afterCommit = options?.afterCommit || "";
  const repoPath = String(options?.repoPath || "").trim();
  const scope = gitScopeKey(rootId, repoPath);
  const cached = getHistoryCacheEntry(scope);
  if (!options?.force && !beforeCommit && !afterCommit && cached) {
    return {
      available: true,
      items: cached.items.slice(0, limit),
      has_more: cached.items.length > limit || cached.hasMore,
      remote_head: cached.remoteHead,
    };
  }
  if (!options?.force && beforeCommit && cached) {
    const index = cached.items.findIndex((item) => item.hash === beforeCommit);
    if (index >= 0) {
      const cachedPage = cached.items.slice(index + 1, index + 1 + limit);
      if (cachedPage.length > 0 || !cached.hasMore) {
        return { available: true, items: cachedPage, has_more: cached.hasMore, remote_head: cached.remoteHead };
      }
    }
  }

  const key = `${scope}:${beforeCommit}:${afterCommit}:${limit}`;
  const inflight = gitHistoryInflight.get(key);
  if (inflight) {
    return inflight;
  }
  const promise = protectedJSON<any>(
    appURL(
      "/api/git/history",
      withRepoPath(
        new URLSearchParams({
          root: rootId,
          limit: String(limit),
          ...(beforeCommit ? { before_commit: beforeCommit } : {}),
          ...(afterCommit ? { after_commit: afterCommit } : {}),
        }),
        repoPath,
      ),
    ),
  ).then((payload) => {
    const normalized = normalizeGitHistoryPayload(payload);
    if (normalized.commit_missing) {
      clearGitHistoryCache(rootId, { repoPath });
      return normalized;
    }
    const existing = getHistoryCacheEntry(scope);
    if (!normalized.available) {
      return normalized;
    }
    if (!beforeCommit && !afterCommit) {
      const remoteHead = normalized.remote_head;
      setHistoryCacheEntry(scope, {
        items: applyRemoteHead(normalized.items.slice(), remoteHead),
        hasMore: normalized.has_more,
        remoteHead,
      });
    } else if (beforeCommit) {
      const remoteHead = normalized.remote_head || existing?.remoteHead;
      setHistoryCacheEntry(scope, {
        items: applyRemoteHead(mergeHistoryItems(existing?.items || [], normalized.items), remoteHead),
        hasMore: normalized.has_more,
        remoteHead,
      });
    } else if (afterCommit) {
      // afterCommit is a change probe. A reset/rebase can leave afterCommit as an
      // existing object that is no longer in the current HEAD history, so blindly
      // prepending new commits to the cached list can show stale commits.
    }
    return {
      ...normalized,
      items: applyRemoteHead(normalized.items, normalized.remote_head),
    };
  }).finally(() => {
    gitHistoryInflight.delete(key);
  });
  gitHistoryInflight.set(key, promise);
  return promise;
}

export async function fetchGitCommitFiles(
  rootId: string,
  commit: string,
  options?: GitRepoScope,
): Promise<GitCommitFilesPayload> {
  const repoPath = String(options?.repoPath || "").trim();
  const scope = gitScopeKey(rootId, repoPath);
  const key = `${scope}:${commit}`;
  const cached = gitCommitFilesCache.get(key);
  if (cached) {
    return cached;
  }
  const persisted = readStorageJSON<GitCommitFilesPayload>(commitFilesStorageKey(scope, commit));
  if (persisted && Array.isArray(persisted.items)) {
    gitCommitFilesCache.set(key, persisted);
    return persisted;
  }
  const inflight = gitCommitFilesInflight.get(key);
  if (inflight) {
    return inflight;
  }
  const promise = protectedJSON<any>(
    appURL("/api/git/commit/files", withRepoPath(new URLSearchParams({ root: rootId, commit }), repoPath)),
  ).then((payload) => {
    const normalized = {
      commit: typeof payload?.commit === "string" ? payload.commit : commit,
      items: Array.isArray(payload?.items) ? payload.items as GitStatusItem[] : [],
    };
    gitCommitFilesCache.set(key, normalized);
    writeStorageJSON(commitFilesStorageKey(scope, commit), normalized);
    return normalized;
  }).finally(() => {
    gitCommitFilesInflight.delete(key);
  });
  gitCommitFilesInflight.set(key, promise);
  return promise;
}

export async function fetchGitBranches(rootId: string, options?: GitRepoScope): Promise<GitBranchesPayload> {
  const params = withRepoPath(new URLSearchParams({ root: rootId }), options?.repoPath);
  const payload = await protectedJSON<any>(appURL("/api/git/branches", params));
  return {
    current: typeof payload?.current === "string" ? payload.current : undefined,
    branches: Array.isArray(payload?.branches)
      ? payload.branches
          .map((item: any) => ({
            name: typeof item?.name === "string" ? item.name : "",
            current: item?.current === true,
          }))
          .filter((item: GitBranchItem) => !!item.name)
      : [],
  };
}

export async function checkoutGitBranch(
  rootId: string,
  branch: string,
  options?: GitRepoScope,
): Promise<GitStatusPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/checkout"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath, { branch }),
  });
  return normalizeGitStatusPayload(payload?.status || {});
}

/**
 * Body for a repo-scoped write. An omitted repoPath targets the managed root, so
 * callers can pass the value straight through without branching.
 */
function gitActionBody(rootId: string, repoPath: string | undefined, extra?: Record<string, unknown>): string {
  const repo = String(repoPath || "").trim();
  return JSON.stringify({
    root: rootId,
    ...(repo ? { repo_path: repo } : {}),
    ...(extra || {}),
  });
}

export async function pullGit(rootId: string, options?: GitRepoScope): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/pull"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath),
  });
  return normalizeGitActionPayload(payload);
}

export async function pushGit(rootId: string, options?: GitRepoScope): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/push"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath),
  });
  return normalizeGitActionPayload(payload);
}

export async function commitGit(rootId: string, message: string, options?: GitRepoScope): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/commit"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath, { message }),
  });
  return normalizeGitActionPayload(payload);
}

export async function stageGitItem(
  rootId: string,
  item: Pick<GitStatusItem, "path">,
  options?: GitRepoScope,
): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/stage"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath, { path: item.path }),
  });
  return normalizeGitActionPayload(payload);
}

export async function unstageGitItem(
  rootId: string,
  item: Pick<GitStatusItem, "path">,
  options?: GitRepoScope,
): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/unstage"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath, { path: item.path }),
  });
  return normalizeGitActionPayload(payload);
}

export async function discardGitItem(
  rootId: string,
  item: Pick<GitStatusItem, "path" | "status">,
  options?: GitRepoScope,
): Promise<GitActionPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/discard"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: gitActionBody(rootId, options?.repoPath, { path: item.path, status: item.status }),
  });
  return normalizeGitActionPayload(payload);
}

export async function fetchGitWorktrees(rootId: string): Promise<GitWorktreesPayload> {
  const payload = await protectedJSON<any>(appURL("/api/git/worktrees", new URLSearchParams({ root: rootId })));
  return {
    items: Array.isArray(payload?.items)
      ? payload.items
          .map((item: any) => ({
            path: typeof item?.path === "string" ? item.path : "",
            branch: typeof item?.branch === "string" ? item.branch : undefined,
            head: typeof item?.head === "string" ? item.head : undefined,
            current: item?.current === true,
          }))
          .filter((item: GitWorktreeItem) => !!item.path)
      : [],
  };
}

export async function createGitWorktree(input: {
  rootId: string;
  parentPath: string;
  name: string;
  branchMode: "new" | "existing";
  branch?: string;
}): Promise<any> {
  return protectedJSON<any>(appURL("/api/git/worktrees"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      root: input.rootId,
      parent_path: input.parentPath,
      name: input.name,
      branch_mode: input.branchMode,
      branch: input.branch || "",
    }),
  });
}

export async function removeGitWorktree(rootId: string): Promise<any> {
  return protectedJSON<any>(appURL("/api/git/worktrees"), {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ root: rootId }),
  });
}

export function buildGitDiffCacheSignature(item?: Partial<GitStatusItem> | null): string {
  if (!item) {
    return "";
  }
  return [
    item.status || "",
    item.old_path || "",
    Number(item.additions) || 0,
    Number(item.deletions) || 0,
  ].join(":");
}

export async function fetchGitDiff(
  rootId: string,
  path: string,
  options?: { cacheSignature?: string; repoPath?: string },
): Promise<GitDiffPayload> {
  const cacheSignature = options?.cacheSignature || "";
  const repoPath = String(options?.repoPath || "").trim();
  if (!repoPath) {
    const cached = await getCachedGitDiff(rootId, path, cacheSignature);
    if (cached) {
      return cached as GitDiffPayload;
    }
  }

  const params = new URLSearchParams({ root: rootId, path });
  if (repoPath) {
    params.set("repo_path", repoPath);
  }
  const payload = await protectedJSON<any>(appURL("/api/git/diff", params));
  const diff = {
    path: typeof payload?.path === "string" ? payload.path : path,
    display_path: typeof payload?.display_path === "string" ? payload.display_path : undefined,
    old_path: typeof payload?.old_path === "string" ? payload.old_path : undefined,
    status: typeof payload?.status === "string" ? payload.status : "M",
    additions: Number(payload?.additions) || 0,
    deletions: Number(payload?.deletions) || 0,
    content: typeof payload?.content === "string" ? payload.content : "",
    file_meta: Array.isArray(payload?.file_meta) ? payload.file_meta : [],
    source: "worktree" as const,
  };
  if (!repoPath) {
    await setCachedGitDiff(rootId, path, diff, cacheSignature);
  }
  return diff;
}

export async function fetchGitCommitDiff(
  rootId: string,
  commit: string,
  item: Pick<GitStatusItem, "path" | "old_path" | "status" | "additions" | "deletions">,
  options?: GitRepoScope,
): Promise<GitDiffPayload> {
  const path = item.path;
  const repoPath = String(options?.repoPath || "").trim();
  const scope = gitScopeKey(rootId, repoPath);
  const key = `${scope}:${commit}:${item.old_path || ""}:${path}`;
  const cached = gitCommitDiffCache.get(key);
  if (cached) {
    return cached;
  }
  const persisted = readStorageJSON<GitDiffPayload>(commitDiffStorageKey(scope, commit, item.old_path || "", path));
  if (persisted && typeof persisted.content === "string") {
    gitCommitDiffCache.set(key, persisted);
    return persisted;
  }
  const inflight = gitCommitDiffInflight.get(key);
  if (inflight) {
    return inflight;
  }
  const promise = protectedJSON<any>(
    appURL("/api/git/commit/diff", withRepoPath(new URLSearchParams({ root: rootId, commit, path }), repoPath)),
  ).then((payload) => {
    const diff = {
    path: typeof payload?.path === "string" ? payload.path : path,
    display_path: typeof payload?.display_path === "string" ? payload.display_path : undefined,
    old_path: typeof payload?.old_path === "string" ? payload.old_path : item.old_path,
      status: typeof payload?.status === "string" ? payload.status : item.status,
      additions: Number(payload?.additions) || Number(item.additions) || 0,
      deletions: Number(payload?.deletions) || Number(item.deletions) || 0,
      content: typeof payload?.content === "string" ? payload.content : "",
      file_meta: Array.isArray(payload?.file_meta) ? payload.file_meta : [],
      commit,
      source: "commit" as const,
    };
    gitCommitDiffCache.set(key, diff);
    writeStorageJSON(commitDiffStorageKey(scope, commit, item.old_path || "", path), diff);
    return diff;
  }).finally(() => {
    gitCommitDiffInflight.delete(key);
  });
  gitCommitDiffInflight.set(key, promise);
  return promise;
}

export async function fetchGitRelatedFileDiff(
  rootId: string,
  file: { path: string; head?: string; repo_path?: string; repo_kind?: string },
): Promise<GitDiffPayload> {
  const path = file.path;
  const head = file.head || "";
  const params = new URLSearchParams({ root: rootId, path });
  if (head) {
    params.set("head", head);
  }
  if (file.repo_path) {
    params.set("repo_path", file.repo_path);
  }
  if (file.repo_kind) {
    params.set("repo_kind", file.repo_kind);
  }
  const payload = await protectedJSON<any>(appURL("/api/git/related-file/diff", params));
  return {
    path: typeof payload?.path === "string" ? payload.path : path,
    display_path: typeof payload?.display_path === "string" ? payload.display_path : undefined,
    old_path: typeof payload?.old_path === "string" ? payload.old_path : undefined,
    status: typeof payload?.status === "string" ? payload.status : "M",
    additions: Number(payload?.additions) || 0,
    deletions: Number(payload?.deletions) || 0,
    content: typeof payload?.content === "string" ? payload.content : "",
    file_meta: Array.isArray(payload?.file_meta) ? payload.file_meta : [],
    base_head: typeof payload?.base_head === "string" ? payload.base_head : head,
    target_head: typeof payload?.target_head === "string" ? payload.target_head : undefined,
    source: payload?.source === "commit_range" ? "commit_range" : "worktree",
  };
}

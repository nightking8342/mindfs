import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
const tasks = readFileSync(new URL("../src/services/tasks.ts", import.meta.url), "utf8");

// A task deleted while the page was disconnected never appears in the
// incremental `after` result, so merging alone leaves it on the board forever.
// The client must reconcile against the server's full id list.
assert.match(
  tasks,
  /export async function fetchTaskIds\(rootId: string\): Promise<string\[\]> \{[\s\S]*?ids: "1"/,
  "fetchTaskIds should request the full id list with ids=1",
);

assert.match(
  app,
  /const reconcileKanbanTasks = useCallback\(async \(rootId: string, ids: string\[\]\) => \{/,
  "App should have a reconciliation step for task deletions",
);

// Reconciliation must remove the tasks the server no longer reports, from both
// the ref snapshot and every derived state.
assert.match(
  app,
  /const reconcileKanbanTasks[\s\S]*?!keep\.has\(id\)[\s\S]*?taskDetailsByIdRef\.current = nextSnapshot/,
  "reconciliation should drop locally cached tasks missing from the server list",
);
for (const [setter, name] of [
  ["setTaskDetailsById", "task details"],
  ["setKanbanTaskCountItems", "task counts"],
  ["setTaskFirstInputById", "first input"],
  ["setTaskSessionKeysById", "session keys"],
]) {
  const body = app.slice(app.indexOf("const reconcileKanbanTasks"));
  const scoped = body.slice(0, body.indexOf("const loadKanbanTasks"));
  assert.ok(scoped.includes(setter), `reconciliation should clear ${name} (${setter})`);
}

assert.match(
  app,
  /await reconcileKanbanTasks\(targetRoot, await fetchTaskIds\(targetRoot\)\)/,
  "task loading should reconcile against the server id list",
);

// A failed id fetch must not delete anything: reporting nothing as "removed"
// would wipe the board on a transient network error.
assert.match(
  app,
  /reconcileKanbanTasks\(targetRoot, await fetchTaskIds\(targetRoot\)\);[\s\S]{0,200}?catch \(err\) \{\s*console\.error/,
  "a failed id fetch should be swallowed without reconciling",
);

// Reconciliation must also drop the stale rows from the IndexedDB cache,
// otherwise the next load re-applies them.
assert.match(
  tasks,
  /export async function dropCachedTasksExcept\(rootId: string, keep: ReadonlySet<string>\)/,
  "the cache needs a scoped delete to drop reconciled-away tasks",
);
assert.match(
  app,
  /reconcileKanbanTasks[\s\S]*?void dropCachedTasksExcept\(rootId, keep\)/,
  "reconciliation should prune the persistent cache too",
);

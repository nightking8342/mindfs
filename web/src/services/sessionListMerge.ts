export type PinAwareSessionItem = {
  key?: string;
  session_key?: string;
  root_id?: string;
  updated_at?: string;
  pinned_at?: string | null;
};

function sessionKey(item: PinAwareSessionItem): string {
  return String(item.key || item.session_key || "");
}

function timeValue(value: string | null | undefined): number {
  return Date.parse(String(value || "")) || 0;
}

export function mergeSessionItems<T extends PinAwareSessionItem>(
  current: T[],
  incoming: T[],
): T[] {
  const byKey = new Map<string, T>();
  for (const item of current) {
    const key = sessionKey(item);
    if (key) {
      byKey.set(key, item);
    }
  }
  for (const item of incoming) {
    const key = sessionKey(item);
    if (!key) {
      continue;
    }
    const previous = byKey.get(key) as Record<string, unknown> | undefined;
    // 临时诊断探针（pending）：toSessionItem 会对服务端不返回的字段显式写
    // undefined，展开时就覆盖掉了上一轮合并进来的值。这里把「被覆盖掉 true」
    // 的情形上报给探针——它就是「列表呼吸灯消失」的直接证据。
    // 刻意不走 import：本文件被测试用 vm 沙箱以 CJS 方式加载，import 会让
    // session-list-merge.test.mjs 直接报 require is not defined。
    // 排查结束后连同 services/pendingProbe.ts 一起删除。
    if (
      (previous as any)?.pending === true &&
      (item as any)?.pending === undefined
    ) {
      const hook = (globalThis as any).__mindfsPendingWipeHook;
      if (typeof hook === "function") {
        hook(`${(item as any)?.root_id || ""}::${key}`);
      }
    }
    byKey.set(key, { ...(previous || ({} as T)), ...item } as T);
  }
  return Array.from(byKey.values()).sort(compareSessionItems);
}

export function applyPinnedSnapshotToSessions<T extends PinAwareSessionItem>(
  items: T[],
  rootId: string,
  pinnedKeys: string[],
): T[] {
  const pinned = new Set(pinnedKeys.map((key) => String(key || "").trim()).filter(Boolean));
  const next = items.map((item) => {
    if (String(item.root_id || "") !== rootId) {
      return item;
    }
    const key = sessionKey(item);
    if (!key || pinned.has(key) || !item.pinned_at) {
      return item;
    }
    const copy = { ...item };
    delete copy.pinned_at;
    return copy;
  });
  return next.sort(compareSessionItems);
}

function compareSessionItems(left: PinAwareSessionItem, right: PinAwareSessionItem): number {
  const leftPinned = timeValue(left.pinned_at);
  const rightPinned = timeValue(right.pinned_at);
  if (leftPinned || rightPinned) {
    if (leftPinned !== rightPinned) {
      return rightPinned - leftPinned;
    }
  }
  const leftUpdated = timeValue(left.updated_at);
  const rightUpdated = timeValue(right.updated_at);
  if (leftUpdated !== rightUpdated) {
    return rightUpdated - leftUpdated;
  }
  return sessionKey(left).localeCompare(sessionKey(right));
}

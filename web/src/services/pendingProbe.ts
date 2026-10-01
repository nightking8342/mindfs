// 会话「工作状态」诊断探针（临时）。
//
// 背景：左下角「正在生成」、输入框发/停按钮、会话列表呼吸灯读的是三套不同的
// 状态副本（sessionCacheRef / selectedSession / drawerSession / sessions[] /
// multiProjectPendingByKey），历史上有多个互相独立的机制能造成它们不同步。
//
// 采样策略是「按需快照」，不是「全程录像」——因为使用者会挂着几小时才复现，
// 全程流水会撑爆缓冲并把关键的那段挤掉：
//   · 异常记录（副本不一致/误判）  → 全量保留，不淘汰
//   · 常规流水                     → 环形缓冲，只作上下文
//   · 各 tag 计数                  → 聚合，不受淘汰影响
//   · 按下导出那一刻的现场快照      → 才是真正要看的
//
// 开关：localStorage["mindfs-pending-probe"] === "1"
// 面板：右下角的「探针」悬浮按钮（由 ProbePanel 渲染）
//
// 排查完成后整个文件连同各调用点一起删除。

const STORAGE_KEY = "mindfs-pending-probe";
const RECENT_LIMIT = 300;
const SUSPICIOUS_LIMIT = 500;
const STORAGE_ENDPOINT_KEY = "mindfs-pending-probe-endpoint";

type Snapshot = Record<string, unknown>;

export type ProbeSources = {
  /** 某个 root::session 的五份副本取值。 */
  snapshot: (cacheKey: string) => Snapshot;
  /** 当前已知的全部 root::session 键，用于现场快照。 */
  enumerateKeys: () => string[];
  /** 服务端权威视图（/api/replying-sessions），失败时抛错即可。 */
  fetchServerView?: () => Promise<unknown>;
};

let enabled = false;
let seq = 0;
let sources: ProbeSources | null = null;
const recent: Array<Record<string, unknown>> = [];
const suspicious: Array<Record<string, unknown>> = [];
const tagCounts = new Map<string, { total: number; suspicious: number }>();
const lastValues = new Map<string, string>();

try {
  enabled =
    typeof window !== "undefined" &&
    window.localStorage?.getItem(STORAGE_KEY) === "1";
} catch {
  enabled = false;
}

export function pendingProbeEnabled(): boolean {
  return enabled;
}

export function setPendingProbeEnabled(value: boolean): void {
  enabled = value;
  try {
    if (typeof window !== "undefined") {
      window.localStorage.setItem(STORAGE_KEY, value ? "1" : "0");
    }
  } catch {
    // localStorage 不可用时只影响持久化，不影响本次会话内的开关。
  }
}

export function registerProbeSources(next: ProbeSources): void {
  sources = next;
}

function readEndpointOverride(): string {
  try {
    return window.localStorage?.getItem(STORAGE_ENDPOINT_KEY) || "";
  } catch {
    return "";
  }
}

export function setProbeEndpoint(value: string): void {
  try {
    if (value) {
      window.localStorage.setItem(STORAGE_ENDPOINT_KEY, value);
    } else {
      window.localStorage.removeItem(STORAGE_ENDPOINT_KEY);
    }
  } catch {
    // 忽略。
  }
}

/**
 * 默认接收地址：假定接收脚本跑在「本机服务的同一个 IP」上，只是换个端口。
 * APK 里页面来自 MindFS 服务（如 https://192.168.1.5:7766），于是推导出
 * http://192.168.1.5:5174/__probe__，无需任何手工配置。
 */
export function defaultProbeEndpoint(): string {
  const override = readEndpointOverride();
  if (override) {
    return override;
  }
  try {
    const host = window.location.hostname || "localhost";
    return `http://${host}:5174/__probe__`;
  } catch {
    return "http://localhost:5174/__probe__";
  }
}

const COPY_FIELDS = ["cache", "drawer", "selected", "list", "multiProject"] as const;

// 「可疑」= 五份副本对同一个会话给出了不一致的答案（有的说在跑、有的说没在跑），
// 或者命中已知的嫌疑路径。
function isSuspicious(record: Record<string, unknown>): boolean {
  const tag = String(record.tag || "");
  if (tag === "merge.pending.wiped") return true;
  if (tag === "send.isQueueSend" && record.isQueueSend === true) return true;
  if (tag === "restoreActiveSession" && record.serverPending === undefined) return true;
  const detail = record.detail as Record<string, unknown> | undefined;
  if (tag === "stream.done" && detail?.earlyReturn === true) return true;
  if (
    tag === "stream.first" &&
    record.refPending === undefined &&
    record.draftPending === false
  ) {
    return true;
  }
  const values = COPY_FIELDS.map((field) => record[field]);
  const hasTrue = values.some((value) => value === true);
  const hasNotTrue = values.some((value) => value !== true);
  return hasTrue && hasNotTrue;
}

function snapshotOf(cacheKey: string): Snapshot {
  if (!sources || !cacheKey) {
    return {};
  }
  try {
    return sources.snapshot(cacheKey);
  } catch (error) {
    return { snapshotError: String((error as Error)?.message || error) };
  }
}

function record(tag: string, cacheKey: string, entry: Record<string, unknown>): void {
  if (!enabled) {
    return;
  }
  const counts = tagCounts.get(tag) || { total: 0, suspicious: 0 };
  counts.total += 1;
  const flagged = isSuspicious(entry);
  if (flagged) {
    counts.suspicious += 1;
  }
  tagCounts.set(tag, counts);

  if (flagged) {
    suspicious.push(entry);
    if (suspicious.length > SUSPICIOUS_LIMIT) {
      suspicious.shift();
    }
  }
  recent.push(entry);
  if (recent.length > RECENT_LIMIT) {
    recent.shift();
  }
  // 可疑记录始终打印；常规记录只在控制台留档，避免真机上刷屏。
  if (flagged) {
    console.warn(`[pending-probe] 异常 #${entry.seq}`, entry);
  }
}

/** 状态迁移点：记录事件 + 该会话五份副本的即时取值。 */
export function logPending(
  tag: string,
  cacheKey: string,
  detail?: Record<string, unknown>,
): void {
  if (!enabled) {
    return;
  }
  record(tag, cacheKey, {
    seq: ++seq,
    at: new Date().toISOString(),
    tag,
    key: cacheKey,
    ...(detail ? { detail } : {}),
    ...snapshotOf(cacheKey),
  });
}

/** 渲染期读取点：同一 tag+key 取值不变时静默，避免刷屏。 */
export function logPendingValue(
  tag: string,
  cacheKey: string,
  value: unknown,
  detail?: Record<string, unknown>,
): void {
  if (!enabled) {
    return;
  }
  const dedupeKey = `${tag}::${cacheKey}`;
  const serialized = JSON.stringify(value ?? null);
  if (lastValues.get(dedupeKey) === serialized) {
    return;
  }
  lastValues.set(dedupeKey, serialized);
  record(tag, cacheKey, {
    seq: ++seq,
    at: new Date().toISOString(),
    tag,
    key: cacheKey,
    value,
    ...(detail ? { detail } : {}),
    ...snapshotOf(cacheKey),
  });
}

export function probeAggregates(): Array<Record<string, unknown>> {
  return Array.from(tagCounts.entries()).map(([tag, counts]) => ({ tag, ...counts }));
}

export function probeSuspiciousRecords(): Array<Record<string, unknown>> {
  return suspicious;
}

async function captureServerView(): Promise<unknown> {
  if (!sources?.fetchServerView) {
    return { error: "no fetchServerView registered" };
  }
  try {
    return await sources.fetchServerView();
  } catch (error) {
    return { error: String((error as Error)?.message || error) };
  }
}

function captureLiveSnapshot(): {
  keys: string[];
  sessions: Record<string, Snapshot>;
} {
  const keys = sources?.enumerateKeys() || [];
  const sessions: Record<string, Snapshot> = {};
  for (const key of keys) {
    sessions[key] = snapshotOf(key);
  }
  return { keys, sessions };
}

/**
 * 组装上报包。
 *
 * 刻意只包含「现场快照 + 异常记录」，常规流水只带最近若干条作上下文——
 * 使用者可能挂了几小时才导出，全程流水既体积大又早已把关键段挤掉。
 */
export async function buildProbeBundle(): Promise<Record<string, unknown>> {
  const live = captureLiveSnapshot();
  // 现场快照里若出现副本不一致，单独列出来——这是最直接的证据。
  const inconsistent = live.keys.filter((key) => {
    const entry = live.sessions[key] || {};
    const values = COPY_FIELDS.map((field) => entry[field]);
    return values.some((v) => v === true) && values.some((v) => v !== true);
  });
  return {
    exportedAt: new Date().toISOString(),
    probeVersion: 2,
    location: typeof window !== "undefined" ? window.location.href : "",
    environment: {
      userAgent: typeof navigator !== "undefined" ? navigator.userAgent : "",
      platform: typeof navigator !== "undefined" ? navigator.platform : "",
      viewport: typeof window !== "undefined"
        ? `${window.innerWidth}x${window.innerHeight}`
        : "",
      devicePixelRatio: typeof window !== "undefined" ? window.devicePixelRatio : 0,
      // 复现经常和「切后台/断网」相关，这两项要一起记。
      visibility: typeof document !== "undefined" ? document.visibilityState : "",
      online: typeof navigator !== "undefined" ? navigator.onLine : null,
      crossProjectMode:
        (() => {
          try {
            return window.localStorage?.getItem("mindfs-multi-project-session-list");
          } catch {
            return null;
          }
        })(),
    },
    counts: {
      keys: live.keys.length,
      suspicious: suspicious.length,
      recent: recent.length,
    },
    /** 各判定的触发次数——不受环形淘汰影响，是「有没有发生过」的可靠依据。 */
    aggregates: probeAggregates(),
    /** 现场快照里副本互相矛盾的会话键。 */
    inconsistentNow: inconsistent,
    serverView: await captureServerView(),
    liveSnapshot: live,
    suspicious,
    recent,
  };
}

/** 上报到本机接收脚本，由它落盘。返回落盘路径或错误。 */
export async function sendProbeBundle(
  endpoint: string,
): Promise<{ ok: boolean; detail: string }> {
  if (!enabled) {
    return { ok: false, detail: "探针未启用" };
  }
  let bundle: Record<string, unknown>;
  try {
    bundle = await buildProbeBundle();
  } catch (error) {
    return { ok: false, detail: `组装失败: ${String((error as Error)?.message || error)}` };
  }
  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(bundle),
    });
    if (!response.ok) {
      return { ok: false, detail: `HTTP ${response.status}` };
    }
    const text = await response.text();
    return { ok: true, detail: text || "ok" };
  } catch (error) {
    return {
      ok: false,
      detail: `无法连接 ${endpoint}（${String((error as Error)?.message || error)}）`,
    };
  }
}

/** 兜底导出：直接下载文件（浏览器/电脑端可用）。 */
export async function downloadProbeBundle(): Promise<{ ok: boolean; detail: string }> {
  try {
    const bundle = await buildProbeBundle();
    const blob = new Blob([JSON.stringify(bundle, null, 2)], {
      type: "application/json",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `mindfs-pending-probe-${Date.now()}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
    return { ok: true, detail: "已触发下载" };
  } catch (error) {
    return { ok: false, detail: String((error as Error)?.message || error) };
  }
}

export function clearProbeRecords(): void {
  recent.length = 0;
  suspicious.length = 0;
  tagCounts.clear();
  lastValues.clear();
  seq = 0;
}

declare global {
  interface Window {
    __mindfsPendingProbe?: Record<string, unknown>;
  }
}

if (typeof window !== "undefined") {
  window.__mindfsPendingProbe = {
    enabled: () => enabled,
    on: () => setPendingProbeEnabled(true),
    off: () => setPendingProbeEnabled(false),
    clear: clearProbeRecords,
    aggregates: probeAggregates,
    suspicious: probeSuspiciousRecords,
    bundle: buildProbeBundle,
    send: (endpoint?: string) => sendProbeBundle(endpoint || defaultProbeEndpoint()),
    download: downloadProbeBundle,
  };
}

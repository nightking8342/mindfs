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
// 去噪之后正常使用下的产率约「每次回复几条」，2000 条足以覆盖数小时。
// 之前是 300 条，被逐分片事件冲得只剩几秒 —— 那是设计错误，不是数据问题。
const RECENT_LIMIT = 2000;
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
/** 触顶后被丢弃的异常条数，导出时一并带上，避免把截断误读成完整。 */
let truncatedSuspicious = 0;

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

/** 快照里记录的全部副本字段（用于采集/展示）。 */
const COPY_FIELDS = ["cache", "drawer", "selected", "list", "multiProject"] as const;
/** 参与「本地是否声称在跑」判定的字段：只取真正会驱动三处显示的那几份。 */
const LOADED_COPY_FIELDS = ["cache", "selected", "drawer", "multiProject"] as const;

/**
 * 逐事件记录会刷屏的 tag：它们每个流式分片都会触发，一次长回复几千条，
 * 会把环形缓冲冲得只剩几秒。这些 tag 只累加计数、不写流水（除非命中异常）。
 */
const NOISY_TAGS = new Set(["stream.first", "mark.pending"]);

// 「可疑」= 五份副本对同一个会话给出了不一致的答案（有的说在跑、有的说没在跑），
// 或者命中已知的嫌疑路径。
function isSuspicious(record: Record<string, unknown>): boolean {
  const tag = String(record.tag || "");
  const detail = record.detail as Record<string, unknown> | undefined;

  // ── 判定原则（第一版筛子吃过亏，见下）────────────────────────────
  // ① 五份副本**本来就允许不同**：cache / drawer / selected / list 分属不同
  //    容器，同一会话在「抽屉开着」和「主视图选中」下状态不同是正常的。
  //    把「五份取值不一致」当异常，会在一次长回复里刷出上百条误报。
  // ② undefined ≠ false。快照里省略的字段表示「这份副本没有表态」，
  //    不能当成「它说没在跑」。
  // ③ 只保留「有明确因果、命中即说明某条路径出错」的判据。
  // ────────────────────────────────────────────────────────────────

  // 列表合并把已有 pending:true 冲成 undefined —— 直接对应「呼吸灯消失」。
  if (tag === "merge.pending.wiped") return true;

  // 排队误判：会用「发送前的本地 pending」决定这条消息要不要排队。
  if (tag === "send.isQueueSend" && record.isQueueSend === true) return true;

  // 结束事件走了「还有排队」的早退分支 —— 对应「结束了还显示正在生成」。
  if (tag === "stream.done" && detail?.earlyReturn === true) return true;

  // stream.orphan（拦掉无人认领的事件）曾用于定位子代理串台，但那个守卫
  // 本身就拦掉了真事件（一次上报里 500 条被拦、全是服务端在跑的会话），
  // 已回退。判据保留在此仅作注释，不再有代码产生该 tag。

  // 注：「服务端说没在跑、本地说有」这条判据不在单条记录上做——该函数
  // 跑在同步的流式热路径里，拿不到服务端视图。改在导出时统一比对
  // （见 buildProbeBundle 的 zombieKeys），那里两侧数据都是完整的。
  return false;
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

function bumpCount(tag: string, flagged: boolean): void {
  const counts = tagCounts.get(tag) || { total: 0, suspicious: 0 };
  counts.total += 1;
  if (flagged) {
    counts.suspicious += 1;
  }
  tagCounts.set(tag, counts);
}

function record(tag: string, cacheKey: string, entry: Record<string, unknown>): void {
  if (!enabled) {
    return;
  }
  const flagged = isSuspicious(entry);
  bumpCount(tag, flagged);

  if (flagged) {
    // 异常记录**刻意不淘汰**：挂几小时后回看时，最想看的恰恰是最早那次
    // 异常是怎么发生的，丢掉头部等于把线索丢了。上限只作内存兜底，
    // 触顶时丢的是最新的（越靠后越可能是「同一状态的重复采样」），
    // 并在导出里标注截断，避免误以为完整。
    if (suspicious.length < SUSPICIOUS_LIMIT) {
      suspicious.push(entry);
    } else {
      truncatedSuspicious += 1;
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
  const payload = {
    seq: ++seq,
    at: new Date().toISOString(),
    tag,
    key: cacheKey,
    ...(detail ? { detail } : {}),
    ...snapshotOf(cacheKey),
  };
  // 高频状态迁移点（每个流式分片都会调）默认不写流水，只累加计数——
  // 否则一次长回复就能把环形缓冲冲干净。长回复实测：300 条缓冲只装得下
  // 8 秒，而「一直显示正在生成」这类持续状态是从几分钟前开始的，
  // 导出时早已被冲掉。异常项仍然照常保留。
  if (NOISY_TAGS.has(tag) && !isSuspicious(payload)) {
    bumpCount(tag, false);
    return;
  }
  record(tag, cacheKey, payload);
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
  const serverView = await captureServerView();

  // 服务端权威视图：它说哪些会话正在跑。
  const authoritative = new Set<string>();
  if (Array.isArray(serverView)) {
    for (const item of serverView as Array<Record<string, unknown>>) {
      const rootId = String(item?.rootId || item?.root_id || "");
      const sessionKey = String(item?.sessionKey || item?.session_key || "");
      if (rootId && sessionKey) {
        authoritative.add(`${rootId}::${sessionKey}`);
      }
    }
  }

  /**
   * 僵尸 pending：服务端说这个会话**不在跑**，而本地至少一份会被渲染的副本
   * 仍标着 true。这才是真正要找的东西——三处显示不同步的持久形态。
   *
   * 第一版筛子拿「五份副本互相比」当判据，结果把「抽屉开着而主视图没开」
   * 这类正常差异全算成异常（一次上报刷出上百条误报）。现在就一个基准：
   * 服务端。本地副本之间怎么不一致都无所谓。
   */
  const zombieKeys = live.keys.filter((key) => {
    if (authoritative.has(key)) {
      return false;
    }
    const entry = live.sessions[key] || {};
    // streamActive 必须一起看：实测「Agent 输出完仍显示正在生成」正是
    // isStreaming 单独撑起来的（pending 已 false），只看 pending 会漏判。
    return (
      LOADED_COPY_FIELDS.some((field) => entry[field] === true) ||
      entry.streamActive === true
    );
  });

  /**
   * 反向：服务端说在跑，本地却一份都不认。会让「呼吸灯亮着、但左下角没有
   * 正在生成、按钮还是发送」——即症状 1 的形态。
   */
  const missedByLocal = Array.from(authoritative).filter((key) => {
    const entry = live.sessions[key];
    if (!entry) {
      return true;
    }
    // 与 zombieKeys 对称：streamActive 也算「本地认了」。
    return (
      LOADED_COPY_FIELDS.every((field) => entry[field] !== true) &&
      entry.streamActive !== true
    );
  });

  return {
    exportedAt: new Date().toISOString(),
    probeVersion: 3,
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
      // > 0 说明异常记录触顶被截断，结尾那部分是残缺的。
      truncatedSuspicious,
    },
    /** 各判定的触发次数——不受环形淘汰影响，是「有没有发生过」的可靠依据。 */
    aggregates: probeAggregates(),
    /** 服务端说没在跑、本地仍标在跑 → 僵尸 pending（症状 3 的持久形态）。 */
    zombieKeys,
    /** 服务端说在跑、本地一份都不认 → 三处显示不出来（症状 1 的形态）。 */
    missedByLocal,
    serverView,
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
  truncatedSuspicious = 0;
}

/** 从服务端视图里取出「正在跑」的会话键集合。 */
function authoritativeKeys(serverView: unknown): Set<string> | null {
  if (!Array.isArray(serverView)) {
    return null;
  }
  const keys = new Set<string>();
  for (const item of serverView as Array<Record<string, unknown>>) {
    const rootId = String(item?.rootId || item?.root_id || "");
    const sessionKey = String(item?.sessionKey || item?.session_key || "");
    if (rootId && sessionKey) {
      keys.add(`${rootId}::${sessionKey}`);
    }
  }
  return keys;
}

function localClaimingKeys(): string[] {
  if (!sources) {
    return [];
  }
  const claiming: string[] = [];
  for (const key of sources.enumerateKeys()) {
    const entry = snapshotOf(key);
    // streamActive 必须一起算：实测「结束了仍显示正在生成」是 isStreaming
    // 单独撑起来的（isAwaiting 已经 false），只盯 pending 会漏掉它。
    if (
      LOADED_COPY_FIELDS.some((field) => entry[field] === true) ||
      entry.streamActive === true
    ) {
      claiming.push(key);
    }
  }
  return claiming.sort();
}

let sweepCount = 0;

/**
 * 周期采样：把「此刻哪些会话被本地认为在跑」记进流水，每 30 秒再和服务端对一次。
 *
 * 这是针对「一直显示正在生成」这类**持续状态**的：它没有新的状态迁移，只在
 * 渲染点被反复读取，而渲染点做了取值去重 —— 唯一那条记录被缓冲冲掉后，
 * 导出时就看不到它曾经持续过。周期采样保证整个过程都有迹可循。
 *
 * 每 30 秒对一次服务端，是为了在**问题还在的时候**就留下「服务端说没在跑、
 * 本地却说在跑」的铁证。等到导出时往往已经发过新消息、现场被新一轮覆盖，
 * 那时再比对就晚了。
 * 本地取值不变时静默，所以稳态下不产生噪音。
 */
async function periodicSample(): Promise<void> {
  if (!enabled || !sources) {
    return;
  }
  const claiming = localClaimingKeys();
  const signature = `L:${claiming.join(",")}`;
  const changed = lastValues.get("__sweep_local__") !== signature;
  if (changed) {
    lastValues.set("__sweep_local__", signature);
    record("sweep.local", "", {
      seq: ++seq,
      at: new Date().toISOString(),
      tag: "sweep.local",
      key: "",
      detail: { claiming, count: claiming.length },
    });
  }

  sweepCount += 1;
  if (sweepCount % 6 !== 0) {
    return;
  }
  const serverKeys = authoritativeKeys(await captureServerView());
  if (!serverKeys) {
    return;
  }
  const zombies = claiming.filter((key) => !serverKeys.has(key));
  const missed = Array.from(serverKeys).filter(
    (key) => !claiming.includes(key),
  );
  const serverSignature = `S:${zombies.join(",")}|${missed.join(",")}`;
  if (lastValues.get("__sweep_server__") === serverSignature) {
    return;
  }
  lastValues.set("__sweep_server__", serverSignature);
  record("sweep.server", "", {
    seq: ++seq,
    at: new Date().toISOString(),
    tag: "sweep.server",
    key: "",
    detail: {
      serverReplying: Array.from(serverKeys),
      localClaiming: claiming,
      // 服务端说没在跑、本地说在跑 → 僵尸 pending
      zombieKeys: zombies,
      // 服务端说在跑、本地不认 → 三处显示不出来
      missedByLocal: missed,
    },
  });
}

if (typeof window !== "undefined") {
  window.setInterval(() => {
    void periodicSample();
  }, 5000);
}

/**
 * 发送时刻的服务端对照。
 *
 * 用户的判断方式是「发送后立刻执行 = 服务端其实没在跑」。这里在发送的那一刻
 * 直接问服务端，把「界面说在跑」和「服务端说没在跑」钉在同一时间点上，
 * 不必等到导出时再猜。
 */
export async function logSendServerCheck(
  cacheKey: string,
  detail: Record<string, unknown>,
): Promise<void> {
  if (!enabled) {
    return;
  }
  const serverKeys = authoritativeKeys(await captureServerView());
  if (!serverKeys) {
    return;
  }
  logPending("send.serverCheck", cacheKey, {
    ...detail,
    serverSaysReplying: serverKeys.has(cacheKey),
  });
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

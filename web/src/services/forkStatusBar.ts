import { APPEARANCE_CHANGE_EVENT } from "./appearance";
import { SCHEME_CHANGE_EVENT } from "./forkScheme";

/**
 * fork：应用内切换主题后同步 Android 原生状态栏。
 *
 * 为什么需要这个文件 ——
 *
 * Android 壳（MainActivity）在 decorView 顶部盖了一块 statusBarScrim View 充当
 * 状态栏背景，因为 edge-to-edge 下 window.setStatusBarColor() 已经不起作用。
 * 这块 scrim 的颜色只有两个来源：
 *
 *   applySystemBarStyle()  —— 按**系统** uiMode 取 #020617 / WHITE
 *   MindFSSystemBars 桥    —— 页面主动推
 *
 * 而页面这边只在 onPageFinished 之后被原生拉取过一次（syncStatusBarColorFromPage）。
 * 于是应用内手动换主题时 scrim 保持旧色不动：系统浅色 + 应用内 codex/glass 深色
 * 主题 = 顶上压着一条白边，也就是「状态栏不沉浸」。
 *
 * 上游 main.tsx 里那套 @capacitor/status-bar 同步补不上，两个原因：
 *   1. StatusBar.setBackgroundColor 走的还是 window.setStatusBarColor()，被 scrim
 *      盖住，看不见；
 *   2. 它的 MutationObserver 只盯 data-theme。fork 把明暗拆成了正交的 data-scheme
 *      （见 forkScheme.ts），glass 主题下切明暗时 data-theme 一直是 "glass"，
 *      那个 observer 压根不响应。
 *
 * 所以这里直接走 MindFSSystemBars 桥——它一次把 scrim 背景、window 色和图标明暗
 * 全设了，是当前唯一真正生效的路径。
 *
 * 颜色取每套主题都定义了的 --mindfs-system-bar-bg，**不做 elementFromPoint 采样**：
 * 上游那套采样在 glass 主题下会采到玻璃层的 rgba(255,255,255,0.075)，alpha 被丢掉
 * 后当成纯白，图标于是判成深色，黑底黑字。
 */

type SystemBarsBridge = { setStatusBarColor?: (hex: string) => void };

function systemBarsBridge(): Required<SystemBarsBridge> | null {
  if (typeof window === "undefined") {
    return null;
  }
  const bridge = (window as Window & { MindFSSystemBars?: SystemBarsBridge }).MindFSSystemBars;
  return bridge && typeof bridge.setStatusBarColor === "function"
    ? (bridge as Required<SystemBarsBridge>)
    : null;
}

/** 桥只收 #rrggbb。alpha 按不透明处理——系统栏变量本来就都是实色。 */
function toOpaqueHex(input: string): string | null {
  const value = input.trim();
  if (!value || value === "transparent") {
    return null;
  }
  if (/^#[0-9a-f]{6}$/i.test(value)) {
    return value.toLowerCase();
  }
  const rgb = value.match(/^rgba?\(([^)]+)\)$/i);
  if (!rgb) {
    return null;
  }
  const parts = rgb[1]
    .split(",")
    .slice(0, 3)
    .map((part) => Number.parseFloat(part.trim()));
  if (parts.length !== 3 || parts.some((part) => Number.isNaN(part) || part < 0 || part > 255)) {
    return null;
  }
  return `#${parts.map((part) => Math.round(part).toString(16).padStart(2, "0")).join("")}`;
}

function currentSystemBarColor(): string | null {
  if (typeof document === "undefined") {
    return null;
  }
  const root = getComputedStyle(document.documentElement);
  const declared = toOpaqueHex(root.getPropertyValue("--mindfs-system-bar-bg"));
  if (declared) {
    return declared;
  }
  // 变量缺失只可能是样式还没加载完，按已解析的明暗兜底，别让状态栏卡在上一套配色。
  return document.documentElement.getAttribute("data-scheme") === "light" ? "#ffffff" : "#020617";
}

function pushStatusBarColor(): void {
  const bridge = systemBarsBridge();
  if (!bridge) {
    return;
  }
  const color = currentSystemBarColor();
  if (!color) {
    return;
  }
  try {
    bridge.setStatusBarColor(color);
  } catch {
    // 桥调用失败不该影响页面
  }
}

let queued = false;

function scheduleStatusBarSync(): void {
  if (typeof window === "undefined" || !systemBarsBridge()) {
    return;
  }
  if (!queued) {
    queued = true;
    // 双 rAF：上游 main.tsx 的同步排在单 rAF，让 fork 的值收尾，避免图标明暗被它算错的结果覆盖
    window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        queued = false;
        pushStatusBarColor();
      });
    });
  }
  // 上游那条是跨 Capacitor bridge 的异步调用，落地时刻不确定；原生侧幂等，补推无副作用
  window.setTimeout(pushStatusBarColor, 160);
  window.setTimeout(pushStatusBarColor, 420);
}

if (typeof window !== "undefined" && typeof document !== "undefined") {
  scheduleStatusBarSync();
  window.addEventListener(APPEARANCE_CHANGE_EVENT, scheduleStatusBarSync);
  window.addEventListener(SCHEME_CHANGE_EVENT, scheduleStatusBarSync);
  window.addEventListener("mindfs:native-theme-changed", scheduleStatusBarSync);
  window.addEventListener("mindfs:safe-area-updated", scheduleStatusBarSync);
  window.addEventListener("pageshow", scheduleStatusBarSync);
  window.addEventListener("focus", scheduleStatusBarSync);
  // onResume 里的 applySystemBarStyle() 会把 scrim 打回系统深浅色，回前台时补推纠正
  document.addEventListener("visibilitychange", scheduleStatusBarSync);
  if (typeof MutationObserver === "function") {
    new MutationObserver(scheduleStatusBarSync).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme", "data-scheme"],
    });
  }
}

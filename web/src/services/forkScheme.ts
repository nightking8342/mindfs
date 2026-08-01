import { APPEARANCE_CHANGE_EVENT, getAppearanceMode, type AppearanceMode } from "./appearance";

/**
 * fork 的明暗维度 —— 与主题正交。
 *
 * 上游把「跟随系统」当成一个主题（`system`），这让「meadow 且跟随系统」这类
 * 组合无法表达，而 `dark` / `light` 其实只是同一套默认配色的两个变体。
 * 这里把两件事拆开：
 *
 *   data-theme="glass"   ← 主题（配色方案），仍由上游 appearance.ts 写
 *   data-scheme="dark"   ← 明暗（**已解析的最终值**，不是用户的原始偏好）
 *
 * 由 JS 一次性解析成最终值的好处：CSS 里一条媒体查询都不用写，每套配色值只写
 * 一遍。否则「浅色」「浅色 + 减少透明度」这类条件会变成笛卡尔积。
 *
 * 本模块 import 即自执行（见文件末尾），因此属性在首次渲染前就已就位，无闪烁。
 */

export type SchemePref = "auto" | "light" | "dark";
export type Scheme = "light" | "dark";

export const SCHEME_STORAGE_KEY = "mindfs-fork-color-scheme";
export const SCHEME_CHANGE_EVENT = "mindfs:fork-scheme-changed";

/**
 * 每个主题支持的明暗形态。
 *
 * 只列出会写 data-scheme 的主题。上游的 `system` 刻意不在表内——它要保持
 * 「移除 data-theme，交给 index.css 的媒体查询」这套原有行为，写了反而会干扰。
 */
const THEME_SCHEMES: Partial<Record<AppearanceMode, Scheme[]>> = {
  glass: ["light", "dark"],
  codex: ["dark"],
  dark: ["dark"],
  light: ["light"],
  meadow: ["light"],
  moss: ["light"],
};

const schemePrefs = new Set<SchemePref>(["auto", "light", "dark"]);

/** 该主题是否有明暗两副面孔——只有它才值得给用户开关。 */
export function themeSupportsBothSchemes(mode: AppearanceMode): boolean {
  return (THEME_SCHEMES[mode]?.length ?? 0) > 1;
}

/** 单模式主题固定渲染成哪一种；双模式或未知主题返回 null。 */
export function fixedSchemeOf(mode: AppearanceMode): Scheme | null {
  const supported = THEME_SCHEMES[mode];
  return supported && supported.length === 1 ? supported[0] : null;
}

export function normalizeSchemePref(value: unknown): SchemePref {
  return schemePrefs.has(value as SchemePref) ? (value as SchemePref) : "auto";
}

export function getSchemePref(): SchemePref {
  if (typeof window === "undefined") {
    return "auto";
  }
  try {
    return normalizeSchemePref(window.localStorage.getItem(SCHEME_STORAGE_KEY));
  } catch {
    return "auto";
  }
}

function systemPrefersDark(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return true;
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

/**
 * 解析出最终明暗。返回 null 表示该主题不参与 scheme（如上游的 system）。
 *
 * 单模式主题优先于用户偏好：meadow 只有浅色，系统再深也得渲染浅色，
 * 否则会退化成半套变量的深浅混搭。
 */
export function resolveScheme(
  mode: AppearanceMode = getAppearanceMode(),
  pref: SchemePref = getSchemePref(),
): Scheme | null {
  const supported = THEME_SCHEMES[mode];
  if (!supported || supported.length === 0) {
    return null;
  }
  if (supported.length === 1) {
    return supported[0];
  }
  if (pref === "auto") {
    return systemPrefersDark() ? "dark" : "light";
  }
  return supported.includes(pref) ? pref : supported[0];
}

/** 把解析结果写到 <html> 上。CSS 只认这个属性。 */
export function applyForkScheme(): void {
  if (typeof document === "undefined") {
    return;
  }
  const scheme = resolveScheme();
  if (scheme) {
    document.documentElement.setAttribute("data-scheme", scheme);
  } else {
    document.documentElement.removeAttribute("data-scheme");
  }
}

export function setSchemePref(pref: SchemePref): void {
  if (typeof window === "undefined") {
    return;
  }
  const next = normalizeSchemePref(pref);
  try {
    if (next === "auto") {
      window.localStorage.removeItem(SCHEME_STORAGE_KEY);
    } else {
      window.localStorage.setItem(SCHEME_STORAGE_KEY, next);
    }
  } catch {
    // 存储不可用时仍然让当前页面生效
  }
  applyForkScheme();
  window.dispatchEvent(new CustomEvent<SchemePref>(SCHEME_CHANGE_EVENT, { detail: next }));
  // 上游的 ActionBar 靠 APPEARANCE_CHANGE_EVENT 刷新前景色，明暗变了同样要通知它
  window.dispatchEvent(new CustomEvent<AppearanceMode>(APPEARANCE_CHANGE_EVENT, { detail: getAppearanceMode() }));
}

if (typeof window !== "undefined") {
  applyForkScheme();
  // 主题换了要重算：新主题可能是单模式，或根本不参与 scheme
  window.addEventListener(APPEARANCE_CHANGE_EVENT, applyForkScheme);
  if (typeof window.matchMedia === "function") {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const onSystemChange = () => {
      applyForkScheme();
      // 上游 ActionBar 的系统监听带 mode === "system" 守卫，fork 主题下不会响应，
      // 这里补一次广播让它的 isDark 跟上
      window.dispatchEvent(new CustomEvent<AppearanceMode>(APPEARANCE_CHANGE_EVENT, { detail: getAppearanceMode() }));
    };
    if (typeof media.addEventListener === "function") {
      media.addEventListener("change", onSystemChange);
    } else {
      media.addListener(onSystemChange);
    }
  }
}

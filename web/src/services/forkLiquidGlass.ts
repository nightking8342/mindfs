/**
 * 液态玻璃折射 —— SDF 位移贴图。
 *
 * 关键在于位移场的形状。用 feTurbulence 随机噪声做位移得到的是水波纹，
 * 不是玻璃；真实玻璃是**边缘折射强、中心几乎不变**的透镜。所以这里逐像素
 * 求圆角矩形的有符号距离场（SDF），按到边缘的距离算位移、方向指向中心，
 * 编码进 RGB（R=X，G=Y）后交给 feDisplacementMap 折射背景。
 *
 * 贴图按元素尺寸生成，ResizeObserver 跟随变化；元素本身零改动——
 * 只是被挂上 backdrop-filter: url(#滤镜)。
 *
 * 仅 Chromium 支持 backdrop-filter 引用 SVG 滤镜。Safari / Firefox 下
 * canUseLiquidGlass() 返回 false，调用方保持原本的 blur 效果即可。
 */

const SVG_NS = "http://www.w3.org/2000/svg";
const DEFS_ID = "fork-liquid-glass-defs";

export type LiquidGlassOptions = {
  /** 折射带占半短边的比例：越大，弯曲的范围越宽 */
  bezel?: number;
  /** 折射强度 */
  strength?: number;
  /** 整体放大（放大镜效果），0 为不放大 */
  zoom?: number;
  blur?: number;
  saturate?: number;
};

const DEFAULTS: Required<LiquidGlassOptions> = {
  bezel: 0.5,
  strength: 0.55,
  zoom: 0,
  blur: 4,
  saturate: 160,
};

let uid = 0;

export function canUseLiquidGlass(): boolean {
  if (typeof window === "undefined" || typeof CSS === "undefined" || !CSS.supports) {
    return false;
  }
  // Safari 会接受属性但不实际应用 SVG 滤镜，所以顺带排掉非 Chromium
  const chromium = typeof (window as { chrome?: unknown }).chrome !== "undefined";
  return chromium && CSS.supports("backdrop-filter", "url(#x)");
}

function ensureDefs(): SVGDefsElement {
  let svg = document.getElementById(DEFS_ID) as SVGSVGElement | null;
  if (!svg) {
    svg = document.createElementNS(SVG_NS, "svg") as SVGSVGElement;
    svg.id = DEFS_ID;
    svg.setAttribute("width", "0");
    svg.setAttribute("height", "0");
    svg.setAttribute("aria-hidden", "true");
    svg.style.position = "absolute";
    svg.style.pointerEvents = "none";
    svg.appendChild(document.createElementNS(SVG_NS, "defs"));
    document.body.appendChild(svg);
  }
  return svg.querySelector("defs") as SVGDefsElement;
}

const clamp = (v: number, a: number, b: number) => Math.min(Math.max(v, a), b);

function smoothStep(edge0: number, edge1: number, x: number): number {
  const t = clamp((x - edge0) / (edge1 - edge0), 0, 1);
  return t * t * (3 - 2 * t);
}

/** 圆角矩形有符号距离场：内部为负，边界为 0 */
function roundedRectSDF(x: number, y: number, hw: number, hh: number, r: number): number {
  const qx = Math.abs(x) - hw + r;
  const qy = Math.abs(y) - hh + r;
  return Math.min(Math.max(qx, qy), 0) + Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) - r;
}

function buildDisplacementMap(
  w: number,
  h: number,
  radius: number,
  bezel: number,
  strength: number,
  zoom: number,
): { url: string; scale: number } | null {
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;

  const img = ctx.createImageData(w, h);
  const raw = new Float32Array(w * h * 2);
  const hw = w / 2;
  const hh = h / 2;
  let maxD = 0.0001;

  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const cx = x + 0.5 - hw;
      const cy = y + 0.5 - hh;
      const d = roundedRectSDF(cx, cy, hw, hh, radius);
      // 0 = 深处 → 1 = 边缘；平方一次让中心过渡更平缓
      let m = smoothStep(-bezel, 0, d);
      m = m * m;
      const len = Math.hypot(cx, cy) || 1;
      const dx = -(cx / len) * m * strength * bezel - cx * zoom;
      const dy = -(cy / len) * m * strength * bezel - cy * zoom;
      const i = (y * w + x) * 2;
      raw[i] = dx;
      raw[i + 1] = dy;
      if (Math.abs(dx) > maxD) maxD = Math.abs(dx);
      if (Math.abs(dy) > maxD) maxD = Math.abs(dy);
    }
  }

  const scale = maxD * 2;
  const data = img.data;
  for (let i = 0, p = 0; i < raw.length; i += 2, p += 4) {
    data[p] = clamp(raw[i] / scale + 0.5, 0, 1) * 255;
    data[p + 1] = clamp(raw[i + 1] / scale + 0.5, 0, 1) * 255;
    data[p + 2] = 128;
    data[p + 3] = 255;
  }
  ctx.putImageData(img, 0, 0);
  return { url: canvas.toDataURL(), scale };
}

export type LiquidGlassHandle = { update: () => void; destroy: () => void };

const attached = new WeakMap<HTMLElement, LiquidGlassHandle>();

/** 给单个元素挂上折射。重复调用同一元素返回既有句柄。 */
export function attachLiquidGlass(el: HTMLElement, options: LiquidGlassOptions = {}): LiquidGlassHandle | null {
  if (!canUseLiquidGlass()) return null;
  const existing = attached.get(el);
  if (existing) return existing;

  const opt = { ...DEFAULTS, ...options };
  const defs = ensureDefs();
  const id = `fork-lg-${uid++}`;

  const filter = document.createElementNS(SVG_NS, "filter");
  filter.setAttribute("id", id);
  filter.setAttribute("filterUnits", "userSpaceOnUse");
  filter.setAttribute("color-interpolation-filters", "sRGB");
  const feImage = document.createElementNS(SVG_NS, "feImage");
  feImage.setAttribute("result", "map");
  feImage.setAttribute("preserveAspectRatio", "none");
  const feDisp = document.createElementNS(SVG_NS, "feDisplacementMap");
  feDisp.setAttribute("in", "SourceGraphic");
  feDisp.setAttribute("in2", "map");
  feDisp.setAttribute("xChannelSelector", "R");
  feDisp.setAttribute("yChannelSelector", "G");
  filter.append(feImage, feDisp);
  defs.appendChild(filter);

  let lastKey = "";
  const update = () => {
    const w = Math.round(el.offsetWidth);
    const h = Math.round(el.offsetHeight);
    if (!w || !h) return;
    const cs = getComputedStyle(el);
    const radius = Math.min(parseFloat(cs.borderTopLeftRadius) || 0, w / 2, h / 2);
    const key = `${w}x${h}x${radius}`;
    if (key === lastKey) return; // 尺寸没变就不重算，逐像素循环不便宜
    lastKey = key;

    const bezel = (Math.min(w, h) / 2) * opt.bezel;
    const map = buildDisplacementMap(w, h, radius, bezel, opt.strength, opt.zoom);
    if (!map) return;
    filter.setAttribute("x", "0");
    filter.setAttribute("y", "0");
    filter.setAttribute("width", String(w));
    filter.setAttribute("height", String(h));
    feImage.setAttribute("width", String(w));
    feImage.setAttribute("height", String(h));
    feImage.setAttribute("href", map.url);
    feDisp.setAttribute("scale", map.scale.toFixed(2));

    const css = `url(#${id}) blur(${opt.blur}px) saturate(${opt.saturate}%)`;
    el.style.backdropFilter = css;
    (el.style as CSSStyleDeclaration & { webkitBackdropFilter?: string }).webkitBackdropFilter = css;
    el.dataset.forkLiquidGlass = "";
  };

  const observer = new ResizeObserver(() => update());
  observer.observe(el);
  update();

  const handle: LiquidGlassHandle = {
    update,
    destroy() {
      observer.disconnect();
      filter.remove();
      el.style.backdropFilter = "";
      (el.style as CSSStyleDeclaration & { webkitBackdropFilter?: string }).webkitBackdropFilter = "";
      delete el.dataset.forkLiquidGlass;
      attached.delete(el);
    },
  };
  attached.set(el, handle);
  return handle;
}


export function observeLiquidGlass(
  root: HTMLElement | Document,
  selector: string,
  options: LiquidGlassOptions = {},
): () => void {
  if (!canUseLiquidGlass()) return () => {};
  const handles = new Set<LiquidGlassHandle>();

  const scan = () => {
    root.querySelectorAll<HTMLElement>(selector).forEach((el) => {
      const h = attachLiquidGlass(el, options);
      if (h) handles.add(h);
    });
  };
  scan();

  const mo = new MutationObserver(() => scan());
  mo.observe(root === document ? document.body : (root as HTMLElement), { childList: true, subtree: true });

  return () => {
    mo.disconnect();
    handles.forEach((h) => h.destroy());
    handles.clear();
  };
}

/**
 * 镜面高光：把指针位置写成元素上的 --fork-mx / --fork-my，供 CSS 的
 * ::after 径向渐变定位。用事件委托——一个监听器覆盖所有面板，
 * 不必为每个元素单独绑定，新增节点也自动生效。
 */
export function observeEdgeSpecular(root: HTMLElement | Document, selector: string): () => void {
  if (typeof window === "undefined") return () => {};
  const onMove = (event: Event) => {
    const pointer = event as PointerEvent;
    const target = pointer.target as HTMLElement | null;
    const el = target?.closest?.(selector) as HTMLElement | null;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    el.style.setProperty("--fork-mx", `${((pointer.clientX - rect.left) / rect.width) * 100}%`);
    el.style.setProperty("--fork-my", `${((pointer.clientY - rect.top) / rect.height) * 100}%`);
  };
  root.addEventListener("pointermove", onMove, { passive: true });
  return () => root.removeEventListener("pointermove", onMove);
}

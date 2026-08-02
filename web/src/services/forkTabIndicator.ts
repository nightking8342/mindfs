/**
 * 上游标签栏的滑动指示器 —— DOM 侧增强，零上游 diff。
 *
 * 侧栏的标签栏是 fork 自己的 ForkGlassTabs（滑动指示器 + 弹簧形变），
 * 而首页看板的标签栏长在 App.tsx 上万行 JSX 的中段，换成组件就得动那个
 * 巨型文件、往后每次合并上游都在那儿解冲突。改为在 DOM 层增强：往容器里
 * 注入一枚指示器，盯着 aria-selected 走位，手感与 ForkGlassTabs 同源
 * （弹簧参数都来自 forkSpring）。
 *
 * 分工：上游那层「选中项直接上底色」由 fork-theme.css 抹平，否则会有两层
 * 底色；这里只管指示器本身。指示器的配色走 --fork-tabs-indicator-* 变量，
 * 与侧栏共用一套，fallback 是上游原本的强调色，非 glass 主题下观感不变。
 *
 * 依赖的上游结构只有 role="tablist" + role="tab" + aria-selected 三个 ARIA
 * 属性——比 class 名和 inline style 稳，上游改版式也不容易碰坏。
 */

import {
  springStep,
  deformTargets,
  POS_OMEGA,
  POS_ZETA,
  SCALE_X_OMEGA,
  SCALE_X_ZETA,
  SCALE_Y_OMEGA,
  SCALE_Y_ZETA,
  THRESHOLD,
  type SpringState,
} from "./forkSpring";

const INDICATOR_ATTR = "data-fork-tab-indicator";

type Box = { left: number; top: number; width: number; height: number };

function enhance(list: HTMLElement): () => void {
  if (list.querySelector(`[${INDICATOR_ATTR}]`)) {
    return () => {};
  }

  // 指示器要按容器定位，上游没给 position
  const prevPosition = list.style.position;
  if (getComputedStyle(list).position === "static") {
    list.style.position = "relative";
  }

  const indicator = document.createElement("div");
  indicator.setAttribute(INDICATOR_ATTR, "");
  indicator.setAttribute("aria-hidden", "true");
  indicator.style.cssText = [
    "position:absolute",
    "left:0",
    "top:0",
    "border-radius:6px",
    "background:var(--fork-tabs-indicator-bg, var(--accent-color))",
    "box-shadow:var(--fork-tabs-indicator-shadow, 0 1px 3px rgba(37, 99, 235, 0.28))",
    "backdrop-filter:var(--fork-tabs-indicator-filter, none)",
    "-webkit-backdrop-filter:var(--fork-tabs-indicator-filter, none)",
    "transform-origin:center center",
    "pointer-events:none",
    "opacity:0",
    "z-index:0",
    "will-change:transform, width",
  ].join(";");
  list.insertBefore(indicator, list.firstChild);

  const state = {
    pos: { value: 0, velocity: 0 } as SpringState,
    width: { value: 0, velocity: 0 } as SpringState,
    scaleX: { value: 1, velocity: 0 } as SpringState,
    scaleY: { value: 1, velocity: 0 } as SpringState,
    top: 0,
    height: 0,
    initialized: false,
    raf: 0,
    last: 0,
  };

  /** 容器可横向滚动，而 absolute 是跟着内容走的，测量要把 scroll 量加回去 */
  const measure = (): Box | null => {
    const active = list.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    if (!active) return null;
    const base = list.getBoundingClientRect();
    const rect = active.getBoundingClientRect();
    if (!rect.width || !rect.height) return null;
    return {
      left: rect.left - base.left + list.scrollLeft,
      top: rect.top - base.top + list.scrollTop,
      width: rect.width,
      height: rect.height,
    };
  };

  const paint = () => {
    indicator.style.transform =
      `translate(${state.pos.value}px, ${state.top}px) scale(${state.scaleX.value}, ${state.scaleY.value})`;
    indicator.style.width = `${state.width.value}px`;
    indicator.style.height = `${state.height}px`;
  };

  const animate = () => {
    const target = measure();
    if (!target) {
      indicator.style.opacity = "0";
      return;
    }
    state.top = target.top;
    state.height = target.height;
    indicator.style.opacity = "1";

    // 首次定位不做动画，否则进页面时指示器会从左上角飞过来
    if (!state.initialized) {
      state.initialized = true;
      state.pos.value = target.left;
      state.width.value = target.width;
      paint();
      return;
    }

    const step = (now: number) => {
      const dt = Math.min(0.032, state.last ? (now - state.last) / 1000 : 0.016);
      state.last = now;

      state.pos = springStep(state.pos, target.left, dt, POS_OMEGA, POS_ZETA);
      state.width = springStep(state.width, target.width, dt, POS_OMEGA, POS_ZETA);

      const deform = deformTargets(Math.abs(state.pos.velocity));
      state.scaleX = springStep(state.scaleX, deform.scaleX, dt, SCALE_X_OMEGA, SCALE_X_ZETA);
      state.scaleY = springStep(state.scaleY, deform.scaleY, dt, SCALE_Y_OMEGA, SCALE_Y_ZETA);

      paint();

      const settled =
        Math.abs(state.pos.value - target.left) < THRESHOLD &&
        Math.abs(state.pos.velocity) < THRESHOLD &&
        Math.abs(state.width.value - target.width) < THRESHOLD &&
        Math.abs(state.scaleX.value - 1) < 0.001 &&
        Math.abs(state.scaleY.value - 1) < 0.001;
      if (settled) {
        state.pos.value = target.left;
        state.pos.velocity = 0;
        state.width.value = target.width;
        state.scaleX = { value: 1, velocity: 0 };
        state.scaleY = { value: 1, velocity: 0 };
        state.last = 0;
        paint();
        return;
      }
      state.raf = requestAnimationFrame(step);
    };

    cancelAnimationFrame(state.raf);
    state.last = 0;
    state.raf = requestAnimationFrame(step);
  };

  // aria-selected 变化＝切换标签；childList 变化＝模板增删（标签数量会变）
  const mo = new MutationObserver(() => animate());
  mo.observe(list, {
    attributes: true,
    attributeFilter: ["aria-selected"],
    subtree: true,
    childList: true,
  });

  let ro: ResizeObserver | null = null;
  if (typeof ResizeObserver !== "undefined") {
    // 尺寸变化时重新贴合，但不该演出弹簧动画——直接落位
    ro = new ResizeObserver(() => {
      const target = measure();
      if (!target) return;
      cancelAnimationFrame(state.raf);
      state.pos = { value: target.left, velocity: 0 };
      state.width = { value: target.width, velocity: 0 };
      state.scaleX = { value: 1, velocity: 0 };
      state.scaleY = { value: 1, velocity: 0 };
      state.top = target.top;
      state.height = target.height;
      paint();
    });
    ro.observe(list);
  }

  animate();

  return () => {
    cancelAnimationFrame(state.raf);
    mo.disconnect();
    ro?.disconnect();
    indicator.remove();
    list.style.position = prevPosition;
  };
}

/**
 * 给 root 下所有匹配的标签栏挂上指示器，返回清理函数。
 * 标签栏是异步渲染出来的（看板要等任务模板加载完），所以要持续观察新节点。
 */
export function observeTabIndicators(root: HTMLElement | Document, selector: string): () => void {
  if (typeof window === "undefined") return () => {};
  const cleanups = new Map<HTMLElement, () => void>();

  const scan = () => {
    root.querySelectorAll<HTMLElement>(selector).forEach((list) => {
      if (cleanups.has(list)) return;
      cleanups.set(list, enhance(list));
    });
    // 已消失的节点要收掉，否则观察器会一直挂在游离 DOM 上
    cleanups.forEach((dispose, list) => {
      if (!list.isConnected) {
        dispose();
        cleanups.delete(list);
      }
    });
  };
  scan();

  // 主区的 DOM 在流式输出时变动很频繁，扫描按帧合并，避免每条 mutation 都走一遍
  let scheduled = 0;
  const mo = new MutationObserver(() => {
    if (scheduled) return;
    scheduled = requestAnimationFrame(() => {
      scheduled = 0;
      scan();
    });
  });
  mo.observe(root === document ? document.body : (root as HTMLElement), {
    childList: true,
    subtree: true,
  });

  return () => {
    cancelAnimationFrame(scheduled);
    mo.disconnect();
    cleanups.forEach((dispose) => dispose());
    cleanups.clear();
  };
}

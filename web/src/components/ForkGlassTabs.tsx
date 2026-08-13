import React from "react";
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
} from "../services/forkSpring";

/**
 * 玻璃标签栏 —— 滑动指示器 + 弹簧形变。
 *
 * 上游的标签是「选中项直接上底色」，切换没有任何过渡。这里改成一枚会滑动的
 * 指示器，并复刻琉璃 UI（bilibili liuli-ui）的弹簧物理——参数与实现见
 * services/forkSpring.ts，那里同时喂给 forkTabIndicator（上游标签栏的 DOM
 * 增强），保证两种标签栏手感一致。
 *
 * 「液体感」来自形变与速度联动：滑动越快，指示器横向拉得越长、纵向压得越扁，
 * 停下时弹回原形。纯 CSS 的 cubic-bezier 做不出这种联动，所以用 rAF 跑真弹簧
 * ——只驱动一个元素的 transform，开销可忽略。
 *
 * 标签宽度不等（上游给每个 tab 不同的 flexGrow），因此位置和宽度都要实测，
 * 由 ResizeObserver 跟随侧栏拖拽。
 */

export type ForkGlassTab<T extends string> = {
  value: T;
  label: string;
  /** 沿用上游给各标签的宽度权重 */
  grow?: number;
};

export function ForkGlassTabs<T extends string>({
  tabs,
  value,
  onChange,
  ariaLabel,
  dataOnboarding,
}: {
  tabs: Array<ForkGlassTab<T>>;
  value: T;
  onChange: (next: T) => void;
  ariaLabel?: string;
  /** 透传上游 onboarding 锚点（如 `data-onboarding="project-tabs"`），
   *  fork 用自己的标签栏替换上游 DOM 后，引导按该属性定位，缺了会静默失效。 */
  dataOnboarding?: string;
}) {
  const containerRef = React.useRef<HTMLDivElement | null>(null);
  const buttonRefs = React.useRef<Array<HTMLButtonElement | null>>([]);
  const indicatorRef = React.useRef<HTMLDivElement | null>(null);
  const [geometry, setGeometry] = React.useState<Array<{ left: number; width: number }>>([]);

  const activeIndex = Math.max(0, tabs.findIndex((tab) => tab.value === value));

  // 位置与宽度都得实测：标签宽度不等，且会随侧栏拖拽变化
  React.useLayoutEffect(() => {
    const measure = () => {
      const container = containerRef.current;
      if (!container) return;
      const base = container.getBoundingClientRect();
      setGeometry(
        buttonRefs.current.map((button) => {
          if (!button) return { left: 0, width: 0 };
          const rect = button.getBoundingClientRect();
          return { left: rect.left - base.left, width: rect.width };
        }),
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    if (containerRef.current) observer.observe(containerRef.current);
    buttonRefs.current.forEach((button) => button && observer.observe(button));
    return () => observer.disconnect();
  }, [tabs.length]);

  const springRef = React.useRef({
    pos: { value: 0, velocity: 0 } as SpringState,
    width: { value: 0, velocity: 0 } as SpringState,
    scaleX: { value: 1, velocity: 0 } as SpringState,
    scaleY: { value: 1, velocity: 0 } as SpringState,
    initialized: false,
    raf: 0,
    last: 0,
  });

  React.useEffect(() => {
    const target = geometry[activeIndex];
    const indicator = indicatorRef.current;
    if (!target || !indicator || target.width <= 0) return;

    const state = springRef.current;
    const paint = () => {
      indicator.style.transform =
        `translateX(${state.pos.value}px) scale(${state.scaleX.value}, ${state.scaleY.value})`;
      indicator.style.width = `${state.width.value}px`;
    };

    // 首次定位不做动画，否则进页面时指示器会从左侧飞过来
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

      // 形变与速度联动——这是「液体感」的来源
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
    return () => cancelAnimationFrame(state.raf);
  }, [activeIndex, geometry]);

  return (
    <div
      ref={containerRef}
      role="tablist"
      aria-label={ariaLabel}
      data-fork-glass-tabs=""
      data-onboarding={dataOnboarding}
      style={{
        position: "relative",
        display: "flex",
        alignItems: "center",
        gap: "2px",
        padding: "2px",
        borderRadius: "8px",
        border: "1px solid var(--fork-tabs-border, rgba(100, 116, 139, 0.36))",
        background: "var(--fork-tabs-bg, rgba(148, 163, 184, 0.10))",
        minWidth: 0,
        width: "100%",
        overflow: "hidden",
      }}
    >
      <div
        ref={indicatorRef}
        aria-hidden="true"
        data-fork-glass-tabs-indicator=""
        style={{
          position: "absolute",
          left: 0,
          top: "2px",
          bottom: "2px",
          borderRadius: "6px",
          background: "var(--fork-tabs-indicator-bg, var(--accent-color))",
          boxShadow: "var(--fork-tabs-indicator-shadow, 0 1px 3px rgba(37, 99, 235, 0.28))",
          backdropFilter: "var(--fork-tabs-indicator-filter, none)",
          WebkitBackdropFilter: "var(--fork-tabs-indicator-filter, none)",
          transformOrigin: "center center",
          pointerEvents: "none",
          willChange: "transform, width",
        }}
      />
      {tabs.map((tab, index) => {
        const active = tab.value === value;
        return (
          <button
            key={tab.value}
            ref={(node) => {
              buttonRefs.current[index] = node;
            }}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(tab.value)}
            style={{
              position: "relative",
              zIndex: 1,
              border: "none",
              borderRadius: "6px",
              background: "transparent",
              color: active ? "var(--fork-tabs-active-color, #fff)" : "var(--text-secondary)",
              padding: "3px 5px",
              fontSize: "11px",
              fontWeight: 700,
              lineHeight: "14px",
              cursor: "pointer",
              whiteSpace: "nowrap",
              minWidth: 0,
              flex: `${tab.grow ?? 1} 1 auto`,
              transition: "color 0.18s ease",
            }}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

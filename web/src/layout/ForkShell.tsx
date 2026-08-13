import React, { useState, useEffect, useRef } from "react";
import { useI18n } from "../i18n";
import { AppShell } from "./AppShell";
// import 即自执行：在首次渲染前把 data-scheme 写好，避免明暗闪烁
import "../services/forkScheme";
import { observeEdgeSpecular } from "../services/forkLiquidGlass";
import { observeTabIndicators } from "../services/forkTabIndicator";

/**
 * ForkShell —— fork 自己的布局壳，接管上游 AppShell。
 *
 * 上游 AppShell 只负责摆放五个已经渲染好的 ReactNode 槽位（sidebar / main /
 * rightSidebar / footer / drawer），不含任何业务逻辑。fork 想改布局时接管这一层即可，
 * 不必碰 App.tsx 里上万行的装配代码，把与上游的 diff 压到「一行 import」。
 *
 * 约定：
 *   1. props 类型从上游派生 + 精确契约断言，上游改 AppShellProps 时 tsc 立刻报错。
 *   2. 所有尺寸走 --fork-* CSS 变量（默认值见 styles/fork-theme.css），换布局不用改这里。
 *   3. 各区域挂 data-fork-region / data-fork-viewport，供 fork-theme.css 精准命中。
 *   4. 加 ?forkShell=0 可临时回退上游 AppShell，用于合并上游后对照排查。
 */

type ForkShellProps = {
  sidebar: React.ReactNode;
  main: React.ReactNode;
  rightSidebar?: React.ReactNode;
  footer: React.ReactNode;
  drawer?: React.ReactNode;
  leftOpen?: boolean;
  rightOpen?: boolean;
  onCloseLeft?: () => void;
  onCloseRight?: () => void;
  onOpenLeft?: () => void;
  onOpenRight?: () => void;
  sidebarsSwapped?: boolean;
};

// 上游 AppShellProps 未 export，从组件类型反推，避免为此改动上游文件。
type UpstreamShellProps = React.ComponentProps<typeof AppShell>;

// 严格类型相等：双向 extends 检测不到可选属性的增删（结构类型下两边仍互相兼容），
// 必须借助条件类型的延迟求值来比较，这样 prop 的增删改名和可选性变化都能抓到。
type Equals<X, Y> = (<T>() => T extends X ? 1 : 2) extends <T>() => T extends Y ? 1 : 2 ? true : false;

// 契约守卫：上游给 AppShell 增删改 prop 时，这一行编译失败，提醒同步 ForkShellProps。
const forkShellPropsContract: Equals<ForkShellProps, UpstreamShellProps> = true;
void forkShellPropsContract;

const MOBILE_BREAKPOINT = 768;
const TABLET_BREAKPOINT = 1024;

type Viewport = "mobile" | "tablet" | "desktop";

function useViewport(): Viewport {
  const [viewport, setViewport] = useState<Viewport>("desktop");
  useEffect(() => {
    const checkSize = () => {
      const width = window.innerWidth;
      setViewport(width < MOBILE_BREAKPOINT ? "mobile" : width < TABLET_BREAKPOINT ? "tablet" : "desktop");
    };
    checkSize();
    window.addEventListener("resize", checkSize);
    return () => window.removeEventListener("resize", checkSize);
  }, []);
  return viewport;
}

/** 逃生开关：?forkShell=0 时整壳回退到上游 AppShell。 */
function useUpstreamShellFallback(): boolean {
  const [fallback] = useState(() => {
    if (typeof window === "undefined") {
      return false;
    }
    try {
      return new URLSearchParams(window.location.search).get("forkShell") === "0";
    } catch {
      return false;
    }
  });
  return fallback;
}

/**
 * 当前主题 id。用 MutationObserver 盯 data-theme 而不是监听 appearance 事件——
 * 属性是最终状态，事件可能被别的路径绕过（如 system 模式跟随系统切换）。
 */
function useThemeId(): string {
  const [themeId, setThemeId] = useState(() =>
    typeof document === "undefined" ? "" : document.documentElement.getAttribute("data-theme") || ""
  );
  useEffect(() => {
    const sync = () => setThemeId(document.documentElement.getAttribute("data-theme") || "");
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);
  return themeId;
}

/**
 * 首页看板面板的玻璃效果。
 *
 * 材质本体（全透明 + 六层边缘反光）在 fork-theme.css 里，纯 CSS 即可；
 * 这里只补 JS 非做不可的一件事：把指针位置写成 --fork-mx / --fork-my，
 * 供镜面高光的径向渐变定位。事件委托，一个监听器覆盖所有面板。
 *
 * 折射（SDF 位移贴图）暂不启用——当前方案玻璃本体不模糊也无底色，
 * 叠位移只会让底下的字发虚。forkLiquidGlass 的实现保留备用。
 */
function useGlassPanels(themeId: string): void {
  useEffect(() => {
    if (themeId !== "glass") {
      return;
    }
    const main = document.querySelector<HTMLElement>('[data-fork-region="main"]');
    if (!main) return;
    return observeEdgeSpecular(main, '[style*="rgba(148, 163, 184, 0.06)"]');
  }, [themeId]);
}

/**
 * 上游标签栏（首页看板的模板筛选）的滑动指示器。
 *
 * 不按主题分支：侧栏的 ForkGlassTabs 在所有主题下都是滑动指示器，这里跟着
 * 一致，配色交给 --fork-tabs-indicator-* 变量各主题自己定。排除掉带
 * data-fork-glass-tabs 的容器——那本来就是 ForkGlassTabs，已经自带指示器。
 */
function useTabIndicators(): void {
  useEffect(() => observeTabIndicators(document, '[role="tablist"]:not([data-fork-glass-tabs])'), []);
}

/**
 * 移动端左右滑动唤出侧边栏（抽屉）。
 *
 * 在 main 内容区**任意位置**左右滑动：
 *   向右滑 → 打开左侧抽屉（文件树），向左滑 → 打开右侧抽屉（会话列表）。
 * 不用屏幕边缘（iOS 风格）：边缘滑动会触发手机系统的返回手势。水平位移占主导且
 * 超过阈值才触发；垂直滑动（在滚内容）不触发。
 *
 * 起点落在横向可滚动容器内（看板、代码块、表格）时本次让位给滚动——那些内容要
 * 横向拖拽，唤出侧栏会抢手势。监听挂在 main 上，footer（ActionBar 的「左滑新建
 * 会话」触摸）是 main 的兄弟节点，不经过 main，天然不冲突。仅在 isMobile 生效。
 */
type RevealSideTarget = {
  ref: React.RefObject<HTMLElement | null>;
  wallpaperRef?: React.RefObject<HTMLElement | null> | null;
  onOpen?: () => void;
  onClose?: () => void;
  /** 当前是否打开：决定本次滑动是「唤出」还是「关闭」 */
  open: boolean;
  /** 关闭位百分比：左 -100 / 右 100 */
  closedPercent: number;
  /** 桌面侧栏参与 --fork-glass-gap（缝隙），移动抽屉贴边无 gap */
  hasGap: boolean;
};

/**
 * 屏幕水平滑动唤出 / 关闭侧边栏（移动抽屉 / 桌面侧栏通用，触屏设备）——双向。
 *
 * 在 main 内容区水平滑动：touchmove 实时把侧栏跟手平移，data-dragging 取消过渡。
 * 侧栏**关闭**时往打开方向滑 = 唤出（从关闭位跟手拉出，松手超过视口宽 ~18% 打开，
 * 否则弹回）；侧栏**打开**时往关闭方向滑 = 关闭（从打开位跟手滑出，松手超过阈值
 * 关闭，否则弹回）。往「当前状态的反向」滑（关闭时往关方向、打开时往开方向）忽略。
 * 不用屏幕边缘——边缘会触发手机系统返回手势。
 *
 * splitAtCenter = false（移动端）：任意位置，右滑操作左栏、左滑操作右栏。
 * splitAtCenter = true（宽屏/平板）：左右侧栏可**同时展开**，滑动区域按中间线划分，
 * 左半屏只操作左栏、右半屏只操作右栏，互不干扰。
 *
 * 起点落在横向可滚动容器内（看板、代码块、表格）时本次让位给滚动；垂直主导不接管。
 * 仅响应 touch（触屏设备），鼠标/触控板不受影响。移动端壁纸层（wallpaperRef）
 * 同步平移。监听挂在 main 上，footer（ActionBar 的「左滑新建会话」）是兄弟节点，
 * 天然不冲突。
 */
function useSwipeRevealSidebar(
  mainRef: React.RefObject<HTMLElement | null>,
  left: RevealSideTarget,
  right: RevealSideTarget,
  splitAtCenter: boolean,
): void {
  useEffect(() => {
    const el = mainRef.current;
    if (!el) {
      return;
    }
    const THRESHOLD_RATIO = 0.18;
    let startX = 0;
    let startY = 0;
    let tracking = false;
    let activeSide: "left" | "right" | null = null;
    let mode: "reveal" | "close" | null = null;

    // 从触摸起点向上（到 main 为止）找横向可滚动容器：看板、代码块、表格这类
    // overflow-x auto/scroll 且内容超宽的元素，横向拖拽是它们的功能，不能让位给唤出。
    const isHorizScrollable = (target: Element | null): boolean => {
      let node: Element | null = target;
      while (node && node !== el) {
        const style = getComputedStyle(node);
        const overflowX = style.overflowX;
        if (
          (overflowX === "auto" || overflowX === "scroll" || overflowX === "overlay") &&
          node.scrollWidth > node.clientWidth + 1
        ) {
          return true;
        }
        node = node.parentElement;
      }
      return false;
    };

    const readGapPx = (): number => {
      const gap = getComputedStyle(document.documentElement).getPropertyValue("--fork-glass-gap");
      const px = gap ? Number.parseFloat(gap) : 0;
      return Number.isFinite(px) ? px : 0;
    };

    const applyDrag = (side: "left" | "right", dx: number): void => {
      const target = side === "left" ? left : right;
      const aside = target.ref.current;
      if (!aside) {
        return;
      }
      const gap = target.hasGap ? readGapPx() : 0;
      const w = aside.getBoundingClientRect().width;
      const max = w + gap;
      const isLeft = target.closedPercent < 0;
      if (mode === "reveal") {
        // 从关闭位（±100% ± gap）跟手拉出，offset clamp 到「宽 + gap」= 完全打开
        const offset = isLeft ? Math.max(0, Math.min(dx, max)) : Math.min(0, Math.max(dx, -max));
        const expr = isLeft
          ? `calc(${target.closedPercent}% - ${gap}px + ${offset}px)`
          : `calc(${target.closedPercent}% + ${gap}px + ${offset}px)`;
        aside.style.transform = `translateX(${expr}) translateZ(0)`;
        const wp = target.wallpaperRef?.current;
        if (wp) {
          wp.style.transform = `translateX(${expr})`;
        }
      } else {
        // close：从打开位 0 跟手向关闭方向滑出
        const offset = isLeft ? Math.max(-max, Math.min(0, dx)) : Math.min(max, Math.max(0, dx));
        aside.style.transform = `translateX(${offset}px) translateZ(0)`;
        const wp = target.wallpaperRef?.current;
        if (wp) {
          wp.style.transform = `translateX(${offset}px)`;
        }
      }
    };

    // 锁定目标侧栏并决定本次模式；滑动方向与当前状态不符返回 false（不接管）
    const lockSide = (side: "left" | "right", dx: number): boolean => {
      const target = side === "left" ? left : right;
      const towardOpen = side === "left" ? dx > 0 : dx < 0;
      if (target.open) {
        if (towardOpen) {
          return false; // 已打开还往打开方向滑 → 忽略，不再触发展开
        }
        mode = "close";
      } else {
        if (!towardOpen) {
          return false; // 已关闭还往关闭方向滑 → 忽略
        }
        mode = "reveal";
      }
      activeSide = side;
      return true;
    };

    const onTouchStart = (e: TouchEvent) => {
      const touch = e.touches[0];
      if (!touch) {
        return;
      }
      startX = touch.clientX;
      startY = touch.clientY;
      // 起点在横向可滚动容器内 → 本次让位给滚动，不操作侧栏
      tracking = !isHorizScrollable(e.target as Element | null);
      activeSide = null;
      mode = null;
    };
    const onTouchMove = (e: TouchEvent) => {
      if (!tracking) {
        return;
      }
      const touch = e.touches[0];
      if (!touch) {
        return;
      }
      const dx = touch.clientX - startX;
      const dy = touch.clientY - startY;
      // 垂直主导 = 在滚内容，不接管
      if (Math.abs(dx) < Math.abs(dy)) {
        return;
      }
      e.preventDefault();
      if (activeSide === null) {
        // 候选侧栏：宽屏按触摸起点划分，移动端按方向
        const candidate: "left" | "right" | null = splitAtCenter
          ? (startX < window.innerWidth / 2 ? "left" : "right")
          : (dx > 0 ? "left" : "right");
        if (candidate === null || !lockSide(candidate, dx)) {
          return; // 方向与当前开合状态不符，本次不接管
        }
        const target = candidate === "left" ? left : right;
        target.ref.current?.setAttribute("data-dragging", "");
        target.wallpaperRef?.current?.setAttribute("data-dragging", "");
        applyDrag(candidate, dx);
      } else {
        applyDrag(activeSide, dx);
      }
    };
    const onTouchEnd = (e: TouchEvent) => {
      if (!tracking) {
        return;
      }
      tracking = false;
      if (!activeSide || !mode) {
        activeSide = null;
        mode = null;
        return;
      }
      const target = activeSide === "left" ? left : right;
      target.ref.current?.removeAttribute("data-dragging");
      target.wallpaperRef?.current?.removeAttribute("data-dragging");
      const touch = e.changedTouches[0];
      // 用原始滑动距离 dx 判断，不用 clamp 后的跟手位移——clamp 会停在「侧栏宽 + gap」，
      // 而阈值按视口宽算，宽屏下拖满侧栏也到不了阈值（会永远打不开）。
      const dx = touch ? touch.clientX - startX : 0;
      // 宽屏按「侧栏宽的一半」算阈值：视口太宽，按视口比例（18%）算出来 230px+ 偏大，
      // 而侧栏才 260px 宽，拖过一半即触发更跟手。移动端保持视口宽比例。
      const threshold = splitAtCenter
        ? (target.ref.current ? target.ref.current.getBoundingClientRect().width * 0.5 : window.innerWidth * 0.14)
        : window.innerWidth * THRESHOLD_RATIO;
      const isLeft = target.closedPercent < 0;
      if (mode === "reveal") {
        const shouldOpen = isLeft ? dx > threshold : dx < -threshold;
        if (shouldOpen) {
          // 完成打开：React 更新 open=true，transform 置 0 + transition 动画补全
          target.onOpen?.();
        } else {
          // 弹回关闭位（±100% ± gap）
          const gap = target.hasGap ? readGapPx() : 0;
          const expr = isLeft
            ? `calc(${target.closedPercent}% - ${gap}px)`
            : `calc(${target.closedPercent}% + ${gap}px)`;
          if (target.ref.current) {
            target.ref.current.style.transform = `translateX(${expr}) translateZ(0)`;
          }
          if (target.wallpaperRef?.current) {
            target.wallpaperRef.current.style.transform = `translateX(${expr})`;
          }
        }
      } else {
        // close：向左（左栏）/ 向右（右栏）滑出关闭
        const shouldClose = isLeft ? dx < -threshold : dx > threshold;
        if (shouldClose) {
          target.onClose?.();
        } else {
          // 弹回打开位 0
          if (target.ref.current) {
            target.ref.current.style.transform = "translateX(0) translateZ(0)";
          }
          if (target.wallpaperRef?.current) {
            target.wallpaperRef.current.style.transform = "translateX(0)";
          }
        }
      }
      activeSide = null;
      mode = null;
    };

    el.addEventListener("touchstart", onTouchStart, { passive: true });
    el.addEventListener("touchmove", onTouchMove, { passive: false });
    el.addEventListener("touchend", onTouchEnd, { passive: true });
    el.addEventListener("touchcancel", onTouchEnd, { passive: true });
    return () => {
      el.removeEventListener("touchstart", onTouchStart);
      el.removeEventListener("touchmove", onTouchMove);
      el.removeEventListener("touchend", onTouchEnd);
      el.removeEventListener("touchcancel", onTouchEnd);
    };
  }, [mainRef, left.ref, left.wallpaperRef, left.onOpen, left.onClose, left.open, left.closedPercent, left.hasGap, right.ref, right.wallpaperRef, right.onOpen, right.onClose, right.open, right.closedPercent, right.hasGap, splitAtCenter]);
}

/**
 * 移动端抽屉拖动关闭（drag-to-close + 跟手平移）。
 *
 * 抽屉打开时（pointer-events auto），在抽屉上水平拖动：transform 跟随手指，
 * data-dragging 属性取消过渡（见 fork-theme.css）保证跟手不延迟；松手时若拖出
 * 超过视口宽度的 ~28% 就触发 onClose（移除 data-dragging 恢复过渡，React 把
 * transform 置为 ±100% 动画滑出），否则弹回原位。垂直主导的拖动（在滚抽屉内容）
 * 不接管。壁纸层（drawerWallpaperStyle）同步平移，否则抽屉滑走后壁纸还盖在 main 上。
 */
function useDrawerDrag(
  asideRef: React.RefObject<HTMLElement | null>,
  wallpaperRef: React.RefObject<HTMLElement | null>,
  side: "left" | "right",
  opts: { isMobile: boolean; onClose?: () => void },
): void {
  const { isMobile, onClose } = opts;
  useEffect(() => {
    if (!isMobile) {
      return;
    }
    const aside = asideRef.current;
    if (!aside) {
      return;
    }
    const wallpaper = wallpaperRef.current;
    const THRESHOLD_RATIO = 0.28;
    let startX = 0;
    let startY = 0;
    let dragging = false;
    let finalOffset = 0;

    const onStart = (e: TouchEvent) => {
      const touch = e.touches[0];
      if (!touch) {
        return;
      }
      startX = touch.clientX;
      startY = touch.clientY;
      dragging = true;
      finalOffset = 0;
      // 取消过渡，拖动期间跟手不延迟
      aside.setAttribute("data-dragging", "");
      wallpaper?.setAttribute("data-dragging", "");
    };
    const onMove = (e: TouchEvent) => {
      if (!dragging) {
        return;
      }
      const touch = e.touches[0];
      if (!touch) {
        return;
      }
      const dx = touch.clientX - startX;
      const dy = touch.clientY - startY;
      // 垂直主导 = 在滚抽屉内容，不接管拖动
      if (Math.abs(dx) < Math.abs(dy)) {
        return;
      }
      e.preventDefault();
      // 左抽屉向右拖、右抽屉向左拖都 clamp 到 0，抽屉不能反向超出原位
      finalOffset = side === "left" ? Math.min(0, dx) : Math.max(0, dx);
      aside.style.transform = `translateX(${finalOffset}px) translateZ(0)`;
      if (wallpaper) {
        wallpaper.style.transform = `translateX(${finalOffset}px)`;
      }
    };
    const onEnd = () => {
      if (!dragging) {
        return;
      }
      dragging = false;
      // 恢复过渡：接下来要么弹回、要么 onClose 后 React 把 transform 置为 ±100% 动画滑出
      aside.removeAttribute("data-dragging");
      wallpaper?.removeAttribute("data-dragging");
      const width = window.innerWidth;
      const threshold = width * THRESHOLD_RATIO;
      const closing = side === "left" ? finalOffset < -threshold : finalOffset > threshold;
      if (closing) {
        onClose?.();
      } else {
        // 弹回原位
        aside.style.transform = "translateX(0) translateZ(0)";
        if (wallpaper) {
          wallpaper.style.transform = "translateX(0)";
        }
      }
    };

    aside.addEventListener("touchstart", onStart, { passive: true });
    aside.addEventListener("touchmove", onMove, { passive: false });
    aside.addEventListener("touchend", onEnd, { passive: true });
    aside.addEventListener("touchcancel", onEnd, { passive: true });
    return () => {
      aside.removeEventListener("touchstart", onStart);
      aside.removeEventListener("touchmove", onMove);
      aside.removeEventListener("touchend", onEnd);
      aside.removeEventListener("touchcancel", onEnd);
    };
  }, [isMobile, asideRef, wallpaperRef, side, onClose]);
}

const sidebarStyle: React.CSSProperties = {
  gridArea: "sidebar",
  borderRight: "var(--fork-sidebar-border, 1px solid var(--border-color))",
  overflow: "auto",
  background: "var(--fork-sidebar-bg, var(--mindfs-topbar-bg, var(--sidebar-bg)))",
  display: "flex",
  flexDirection: "column",
  position: "relative",
  zIndex: 10,
};

const mainStyle: React.CSSProperties = {
  gridArea: "main",
  overflow: "hidden",
  padding: "0",
  background: "var(--fork-main-bg, var(--mindfs-topbar-bg, var(--mobile-overlay-bg, var(--content-bg))))",
  display: "flex",
  flexDirection: "column",
  minHeight: 0,
  position: "relative",
  zIndex: 1,
  // layout paint：main 用 padding 推挤动画时，reflow 只限制在 main 内部，
  // 不向外传播到 shell / 侧栏，缩小每帧 layout 成本
  contain: "layout paint",
};

const rightStyle: React.CSSProperties = {
  gridArea: "right",
  borderLeft: "var(--fork-right-border, 1px solid var(--border-color))",
  overflow: "auto",
  background: "var(--fork-right-bg, var(--mindfs-topbar-bg, var(--sidebar-bg)))",
  display: "flex",
  flexDirection: "column",
  position: "relative",
  zIndex: 10,
};

const footerStyle: React.CSSProperties = {
  gridArea: "footer",
  borderTop: "none",
  padding: "0",
  display: "flex",
  alignItems: "flex-end",
  justifyContent: "center",
  background: "var(--fork-footer-bg, var(--mindfs-topbar-bg, var(--mobile-overlay-bg, var(--content-bg))))",
  zIndex: 100,
  minWidth: 0,
};

export function ForkShell(props: ForkShellProps) {
  const {
    sidebar,
    main,
    rightSidebar,
    footer,
    drawer,
    leftOpen = true,
    rightOpen = true,
    onCloseLeft,
    onCloseRight,
    onOpenLeft,
    onOpenRight,
    sidebarsSwapped = false,
  } = props;
  const { t } = useI18n();
  const viewport = useViewport();
  const upstreamFallback = useUpstreamShellFallback();
  const themeId = useThemeId();
  useGlassPanels(themeId);
  useTabIndicators();
  const isMobile = viewport === "mobile";
  const isTablet = viewport === "tablet";
  // 移动端抽屉（main 滑动唤出 + 抽屉拖动关闭）：抽屉 content 常驻、开合用 transform 平移。
  const mainRef = useRef<HTMLElement>(null);
  const leftDrawerRef = useRef<HTMLElement>(null);
  const leftWallpaperRef = useRef<HTMLDivElement>(null);
  const rightDrawerRef = useRef<HTMLElement>(null);
  const rightWallpaperRef = useRef<HTMLDivElement>(null);
  // 液态玻璃主题要求内容从输入栏玻璃下方滚过，因此把 footer 移进主区浮起来。
  // 移动端不这么做：那里 footer 要参与 flex 布局给软键盘让位。
  const floatingFooter = themeId === "glass" && !isMobile;
  // physical* 提前到 fallback 分支之前计算：下面的逻辑依赖它们，
  // 而 hooks 必须在条件返回之前无条件调用。
  const physicalLeftOpen = sidebarsSwapped ? rightOpen : leftOpen;
  const physicalRightOpen = sidebarsSwapped ? leftOpen : rightOpen;
  // main 上滑动唤出侧边栏（打开也跟手，touchmove 实时驱动平移）：移动端驱动抽屉，
  // 桌面/平板驱动桌面侧栏（后者参与 --fork-glass-gap）。handlers 传物理左/右。
  const desktopLeftRef = useRef<HTMLElement>(null);
  const desktopRightRef = useRef<HTMLElement>(null);
  useSwipeRevealSidebar(
    mainRef,
    {
      ref: isMobile ? leftDrawerRef : desktopLeftRef,
      wallpaperRef: isMobile ? leftWallpaperRef : null,
      onOpen: sidebarsSwapped ? onOpenRight : onOpenLeft,
      onClose: sidebarsSwapped ? onCloseRight : onCloseLeft,
      open: physicalLeftOpen,
      closedPercent: -100,
      hasGap: !isMobile,
    },
    {
      ref: isMobile ? rightDrawerRef : desktopRightRef,
      wallpaperRef: isMobile ? rightWallpaperRef : null,
      onOpen: sidebarsSwapped ? onOpenLeft : onOpenRight,
      onClose: sidebarsSwapped ? onCloseLeft : onCloseRight,
      open: physicalRightOpen,
      closedPercent: 100,
      hasGap: !isMobile,
    },
    // 宽屏/平板左右侧栏可同时展开，滑动区域按中间线划分；移动端任意位置按方向
    !isMobile,
  );
  // 抽屉打开后拖动关闭（跟手）：物理左/右
  useDrawerDrag(leftDrawerRef, leftWallpaperRef, "left", {
    isMobile,
    onClose: sidebarsSwapped ? onCloseRight : onCloseLeft,
  });
  useDrawerDrag(rightDrawerRef, rightWallpaperRef, "right", {
    isMobile,
    onClose: sidebarsSwapped ? onCloseLeft : onCloseRight,
  });

  if (upstreamFallback) {
    return <AppShell {...props} />;
  }

  const sidebarWidth = isMobile
    ? "0px"
    : isTablet
      ? "var(--fork-sidebar-width-tablet, 200px)"
      : "var(--fork-sidebar-width, 260px)";
  const rightWidth = isMobile
    ? "0px"
    : rightSidebar
      ? isTablet
        ? "var(--fork-right-width-tablet, 240px)"
        : "var(--fork-right-width, 280px)"
      : "0px";
  const mobileHeight = "var(--mindfs-viewport-height, 100dvh)";
  const physicalLeftWidth = sidebarsSwapped ? rightWidth : sidebarWidth;
  const physicalRightWidth = sidebarsSwapped ? sidebarWidth : rightWidth;
  const physicalLeftContent = sidebarsSwapped ? rightSidebar : sidebar;
  const physicalRightContent = sidebarsSwapped ? sidebar : rightSidebar;
  const physicalLeftClose = sidebarsSwapped ? onCloseRight : onCloseLeft;
  const physicalLeftOpenHandler = sidebarsSwapped ? onOpenRight : onOpenLeft;
  const physicalRightClose = sidebarsSwapped ? onCloseLeft : onCloseRight;
  const physicalRightOpenHandler = sidebarsSwapped ? onOpenLeft : onOpenRight;
  const physicalLeftLabel = sidebarsSwapped ? t("sidebar.session") : t("sidebar.file");
  const physicalRightLabel = sidebarsSwapped ? t("sidebar.file") : t("sidebar.session");

  const shellStyle: React.CSSProperties & {
    "--mindfs-actionbar-bottom-padding"?: string;
  } = {
    display: isMobile ? "flex" : "grid",
    flexDirection: isMobile ? "column" : undefined,
    // 桌面端单列 grid：main 占满。侧栏 absolute + transform 滑入滑出
    // （内容不挤压，合成器动画），main 用 padding-left/right 动画推挤内容——
    // 侧栏边缘与 main 内容边缘同 easing 同 duration，视觉上是「侧栏推开 main」。
    gridTemplateColumns: isMobile ? undefined : "1fr",
    gridTemplateRows: isMobile ? undefined : floatingFooter ? "1fr" : "1fr auto",
    gridTemplateAreas: isMobile
      ? undefined
      : floatingFooter
        ? `"main"`
        : `"main" "footer"`,
    minHeight: isMobile ? mobileHeight : "100vh",
    height: isMobile ? mobileHeight : "100dvh",
    background: isMobile
      ? "var(--fork-shell-bg-mobile, var(--mindfs-topbar-bg, var(--mindfs-system-bar-bg, var(--mobile-overlay-bg, var(--content-bg)))))"
      : "var(--fork-shell-bg, var(--bg-gradient-composite, var(--bg-gradient-start, #f3f4f6)))",
    color: "var(--text-primary)",
    position: "relative",
    width: isMobile ? "100%" : undefined,
    maxWidth: isMobile ? "100%" : undefined,
    paddingTop: isMobile ? "var(--mindfs-safe-area-top, env(safe-area-inset-top, 0px))" : undefined,
    overflow: "hidden",
    isolation: "isolate",
    boxSizing: "border-box",
    "--mindfs-actionbar-bottom-padding": "calc(var(--mindfs-safe-area-bottom) + var(--fork-actionbar-gap, 12px))",
  };

  // 桌面端 main：用 margin-left/right 推挤整个玻璃板。侧栏 absolute 盖在左侧
  // margin 区域（间隙里），main 玻璃板随侧栏滑入/滑出同步移动，两者之间始终保留
  // --fork-glass-gap 壁纸缝隙（不覆盖 main）。非 glass 主题 gap fallback 0，退回
  // 贴边推挤。侧栏自身是 transform 合成动画，不挤压侧栏内容。
  const desktopMainStyle: React.CSSProperties = {
    ...mainStyle,
    // 左右 margin 承担推挤；上下保留 CSS 里 glass 的 --fork-glass-gap（非 glass 0）
    marginTop: "var(--fork-glass-gap, 0px)",
    marginBottom: "var(--fork-glass-gap, 0px)",
    marginLeft: physicalLeftOpen
      ? `calc(var(--fork-glass-gap, 0px) + ${physicalLeftWidth})`
      : "var(--fork-glass-gap, 0px)",
    marginRight: physicalRightOpen
      ? `calc(var(--fork-glass-gap, 0px) + ${physicalRightWidth})`
      : "var(--fork-glass-gap, 0px)",
    transition: [
      "margin-left var(--fork-sidebar-transition, 0.3s cubic-bezier(0.4, 0, 0.2, 1))",
      "margin-right var(--fork-sidebar-transition, 0.3s cubic-bezier(0.4, 0, 0.2, 1))",
    ].join(", "),
  };

  // 桌面端侧栏：absolute 覆盖式，用 transform 平移实现展开/折叠。
  // 合成器动画不触发 layout，实测真实侧栏 DOM 下零掉帧。
  // glass 主题下侧栏玻璃板内缩一个 --fork-glass-gap：左/右/top/bottom 各留 gap，
  // 宽度 = 轨道宽 - gap，让玻璃板右缘恰好落在 --fork-sidebar-width 上（折叠按钮
  // 位置不变），main 玻璃板由 margin 推挤留出缝隙，侧栏不盖 main。非 glass 主题
  // --fork-glass-gap 未定义 fallback 0，行为不变。
  const desktopPaneStyle = (side: "left" | "right"): React.CSSProperties => {
    const open = side === "left" ? physicalLeftOpen : physicalRightOpen;
    const width = side === "left" ? physicalLeftWidth : physicalRightWidth;
    return {
      ...(side === "left" ? sidebarStyle : rightStyle),
      position: "absolute",
      top: "var(--fork-glass-gap, 0px)",
      bottom: "var(--fork-glass-gap, 0px)",
      [side]: "var(--fork-glass-gap, 0px)",
      width: `calc(${width} - var(--fork-glass-gap, 0px))`,
      zIndex: 20,
      overflow: open ? "auto" : "hidden",
      pointerEvents: open ? "auto" : "none",
      // transform 只做 translateX 合成动画；translateZ(0) 提升为合成层。
      // 折叠要完全移出视口，除了自身宽度还要把 left 侧漏出的 gap 一起移走
      transform:
        side === "left"
          ? `translateX(${open ? "0%" : "calc(-100% - var(--fork-glass-gap, 0px))"}) translateZ(0)`
          : `translateX(${open ? "0%" : "calc(100% + var(--fork-glass-gap, 0px))"}) translateZ(0)`,
      willChange: "transform",
      backfaceVisibility: "hidden",
      // 纯 transform 动画。折叠后 translateX(±100%) 滑出视口（shell overflow:hidden
      // 裁掉），不需要 visibility 延迟；pointer-events 已按 open 切换。
      transition: "transform var(--fork-sidebar-transition, 0.3s cubic-bezier(0.4, 0, 0.2, 1))",
    };
  };

  const mobileSidebarStyle = (side: "left" | "right", open: boolean): React.CSSProperties => ({
    position: "fixed",
    top: "var(--mindfs-safe-area-top, env(safe-area-inset-top, 0px))",
    bottom: 0,
    [side]: 0,
    width: "var(--fork-mobile-drawer-width, 75vw)",
    zIndex: 2000,
    // 抽屉是半透明玻璃板，透出背后 drawerWallpaperStyle 铺的壁纸（与 main 同一坐标系），
    // 与桌面侧栏同款材质：background 走 --fork-sidebar-bg（噪点 + 半透明白），
    // backdrop-filter 模糊壁纸、inset 高光由 CSS 规则提供——玻璃感三要素都在。
    background: "var(--fork-sidebar-bg, var(--mindfs-topbar-bg, var(--mobile-sidebar-bg, var(--sidebar-bg))))",
    boxShadow: side === "left" ? "4px 0 24px rgba(0,0,0,0.15)" : "-4px 0 24px rgba(0,0,0,0.15)",
    // 开合平移动画：抽屉 content 常驻、始终挂载，transform 由 open 决定 + transition
    // 滑入滑出；拖动时给元素加 data-dragging（CSS 里 transition: none）取消过渡跟手，
    // 松手后移除恢复过渡——弹回或动画滑出关闭。
    transform: open
      ? "translateX(0) translateZ(0)"
      : `translateX(${side === "left" ? "-100%" : "100%"}) translateZ(0)`,
    transition: "transform 0.22s cubic-bezier(0.2, 0.8, 0.2, 1)",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    borderTopRightRadius: side === "left" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderBottomRightRadius: side === "left" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderTopLeftRadius: side === "right" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderBottomLeftRadius: side === "right" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    willChange: "transform",
    backfaceVisibility: "hidden",
    // 关闭时滑出视口，不能接收触摸
    pointerEvents: open ? "auto" : "none",
  });

  // 抽屉壁纸层：铺在 aside **外面**、抽屉之下，fixed 覆盖抽屉区域，作为抽屉玻璃的
  // backdrop。关键在 background-attachment: fixed——它让渐变相对**视口**定位，而不是
  // 相对壁纸层自身（壁纸层只有 75vw）。这样抽屉区域显示的就是视口里那一小块
  // shell 壁纸，与 main 透出的壁纸同一坐标系；配合抽屉的半透明玻璃 + backdrop-filter，
  // 观感与桌面侧栏透出 shell 壁纸一致。
  // 壁纸层盖住抽屉区域的主视图内容（视觉上本来就该被抽屉盖住），main 其余区域不受
  // 影响，不需要像早期方案那样让 main 整体 opacity 0。
  // backgroundImage/backgroundColor 拆开：background-image 不接受纯色值，光斑渐变
  // 单独走 image，底部纯色单独走 color（变量见 fork-theme.css 的
  // --fork-mobile-drawer-wallpaper-image / --fork-mobile-drawer-wallpaper-color）。
  const drawerWallpaperStyle = (side: "left" | "right", open: boolean): React.CSSProperties => ({
    position: "fixed",
    top: "var(--mindfs-safe-area-top, env(safe-area-inset-top, 0px))",
    bottom: 0,
    [side]: 0,
    width: "var(--fork-mobile-drawer-width, 75vw)",
    backgroundImage: "var(--fork-mobile-drawer-wallpaper-image, none)",
    backgroundColor: "var(--fork-mobile-drawer-wallpaper-color, transparent)",
    backgroundAttachment: "fixed",
    backgroundRepeat: "no-repeat",
    // 与 aside 同款圆角：壁纸层是矩形，不圆角的话会在抽屉圆角外的角落露出一块
    // 不透明的直角背景。圆角裁剪后角落透出背后的 overlay/main，和 aside 圆角外一致。
    borderTopRightRadius: side === "left" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderBottomRightRadius: side === "left" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderTopLeftRadius: side === "right" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    borderBottomLeftRadius: side === "right" ? "var(--fork-mobile-drawer-radius, 14px)" : undefined,
    // 壁纸层跟随抽屉开合/拖动平移：否则抽屉滑出后它还盖在 main 上。
    // 注意与 aside 同 duration/easing，且拖动时也走 data-dragging 取消过渡。
    transform: open ? "translateX(0)" : `translateX(${side === "left" ? "-100%" : "100%"})`,
    transition: "transform 0.22s cubic-bezier(0.2, 0.8, 0.2, 1)",
    zIndex: 1700,
    pointerEvents: "none",
  });

  const overlayStyle: React.CSSProperties = {
    position: "fixed",
    inset: 0,
    background: "var(--fork-overlay-bg, rgba(0,0,0,0.3))",
    zIndex: 1500,
    opacity: isMobile && (leftOpen || rightOpen) ? 1 : 0,
    pointerEvents: isMobile && (leftOpen || rightOpen) ? "auto" : "none",
    transition: "opacity 0.18s ease",
    willChange: "opacity",
    backfaceVisibility: "hidden",
    transform: "translateZ(0)",
  };

  return (
    <div
      className="fork-shell"
      data-fork-shell=""
      data-fork-viewport={viewport}
      data-onboarding="shell"
      style={shellStyle}
    >
      {isMobile && (
        <div
          className="fork-shell__overlay"
          style={overlayStyle}
          onClick={() => {
            onCloseLeft?.();
            onCloseRight?.();
          }}
        />
      )}

      {isMobile ? (
        physicalLeftContent ? (
          <>
            {/* 壁纸层在 aside 之外、抽屉之下：fixed 覆盖抽屉区域，供抽屉玻璃透出
                （见 drawerWallpaperStyle）。z-index 1700，压在 overlay(1500) 之上、抽屉(2000) 之下。
                抽屉 content 常驻、始终挂载，开合走 transform 平移动画。 */}
            <div
              className="fork-shell__drawer-wallpaper fork-shell__drawer-wallpaper--left"
              ref={leftWallpaperRef}
              style={drawerWallpaperStyle("left", physicalLeftOpen)}
            />
            <aside
              className="fork-shell__pane fork-shell__pane--left"
              data-fork-region="left"
              data-fork-open={physicalLeftOpen ? "" : undefined}
              ref={leftDrawerRef}
              style={mobileSidebarStyle("left", physicalLeftOpen)}
            >
              {physicalLeftContent}
            </aside>
          </>
        ) : null
      ) : physicalLeftContent ? (
        <aside
          ref={desktopLeftRef}
          className="fork-shell__pane fork-shell__pane--left"
          data-fork-region="left"
          data-fork-open={physicalLeftOpen ? "" : undefined}
          style={desktopPaneStyle("left")}
        >
          {physicalLeftContent}
        </aside>
      ) : null}

      <main
        ref={mainRef}
        className="fork-shell__main"
        data-fork-region="main"
        style={
          isMobile
            ? {
                ...mainStyle,
                flex: 1,
                minHeight: 0,
                minWidth: 0,
              }
            : floatingFooter
              ? {
                  // 给浮起来的输入栏让出高度：内容在 content box 内滚动，不会被永久遮挡。
                  // 代价是内容不会真的滚到玻璃下方——可用性优先于那点动态效果。
                  ...desktopMainStyle,
                  paddingBottom: "var(--fork-floating-footer-space, 96px)",
                }
              : desktopMainStyle
        }
      >
        {main}
        {/* 抽屉层放在主视图内部，绝对定位时才能精准对齐主视图宽度 */}
        {drawer}
        {floatingFooter ? (
          <div
            className="fork-shell__footer fork-shell__footer--floating"
            data-fork-region="footer"
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              bottom: "var(--fork-floating-footer-bottom, 16px)",
              // main 玻璃板整体被 margin 推挤，footer 作为 main 内部 absolute 子元素
              // 天然跟随玻璃板移动，不需要额外 padding 跟随。只保留原 inset 内边距。
              padding: "0 var(--fork-floating-footer-inset, 24px)",
              zIndex: 5,
              // 容器自身不吃事件，避免左右留白挡住内容；子元素在 fork-theme.css 里恢复
              pointerEvents: "none",
            }}
          >
            {footer}
          </div>
        ) : null}
      </main>

      {isMobile ? (
        physicalRightContent ? (
          <>
            {/* 壁纸层在 aside 之外、抽屉之下：fixed 覆盖抽屉区域，供抽屉玻璃透出
                （见 drawerWallpaperStyle）。z-index 1700，压在 overlay(1500) 之上、抽屉(2000) 之下。
                抽屉 content 常驻、始终挂载，开合走 transform 平移动画。 */}
            <div
              className="fork-shell__drawer-wallpaper fork-shell__drawer-wallpaper--right"
              ref={rightWallpaperRef}
              style={drawerWallpaperStyle("right", physicalRightOpen)}
            />
            <aside
              className="fork-shell__pane fork-shell__pane--right"
              data-fork-region="right"
              data-fork-open={physicalRightOpen ? "" : undefined}
              ref={rightDrawerRef}
              style={mobileSidebarStyle("right", physicalRightOpen)}
            >
              {physicalRightContent}
            </aside>
          </>
        ) : null
      ) : physicalRightContent ? (
        <aside
          ref={desktopRightRef}
          className="fork-shell__pane fork-shell__pane--right"
          data-fork-region="right"
          data-fork-open={physicalRightOpen ? "" : undefined}
          style={desktopPaneStyle("right")}
        >
          {physicalRightContent}
        </aside>
      ) : null}

      {!isMobile ? (
        <>
          <button
            type="button"
            className={`mindfs-sidebar-resize-rail mindfs-sidebar-resize-rail--left${physicalLeftOpen ? " is-open" : " is-closed"}`}
            onClick={physicalLeftOpen ? physicalLeftClose : physicalLeftOpenHandler}
            aria-label={physicalLeftOpen ? t("sidebar.collapse", { label: physicalLeftLabel }) : t("sidebar.expand", { label: physicalLeftLabel })}
            title={physicalLeftOpen ? t("sidebar.collapse", { label: physicalLeftLabel }) : t("sidebar.expand", { label: physicalLeftLabel })}
            style={{
              left: 0,
              cursor: physicalLeftOpen ? "w-resize" : "e-resize",
              // 覆盖式：侧栏 absolute 固定左缘，按钮用 transform 跟随侧栏滑出/滑入，
              // 与侧栏同一 transform 动画（合成器，不触发 layout）。
              // 基类有 translateY(-50%) 垂直居中，必须合成进同一个 transform。
              transform: physicalLeftOpen
                ? `translateY(-50%) translateX(calc(${physicalLeftWidth} - 6px))`
                : "translateY(-50%) translateX(0)",
              transition: "transform var(--fork-sidebar-transition, 0.3s cubic-bezier(0.4, 0, 0.2, 1))",
              willChange: "transform",
            }}
          />
          {physicalRightContent ? (
            <button
              type="button"
              className={`mindfs-sidebar-resize-rail mindfs-sidebar-resize-rail--right${physicalRightOpen ? " is-open" : " is-closed"}`}
              onClick={physicalRightOpen ? physicalRightClose : physicalRightOpenHandler}
              aria-label={physicalRightOpen ? t("sidebar.collapse", { label: physicalRightLabel }) : t("sidebar.expand", { label: physicalRightLabel })}
              title={physicalRightOpen ? t("sidebar.collapse", { label: physicalRightLabel }) : t("sidebar.expand", { label: physicalRightLabel })}
              style={{
                right: 0,
                cursor: physicalRightOpen ? "e-resize" : "w-resize",
                transform: physicalRightOpen
                  ? `translateY(-50%) translateX(calc(-1 * (${physicalRightWidth} - 6px)))`
                  : "translateY(-50%) translateX(0)",
                transition: "transform var(--fork-sidebar-transition, 0.3s cubic-bezier(0.4, 0, 0.2, 1))",
                willChange: "transform",
              }}
            />
          ) : null}
        </>
      ) : null}

      {/* 浮动模式下 grid 已无 footer 区域，这里必须整个不渲染：
          留一个空 footer 会被 grid 自动排布，挤乱三栏。 */}
      {floatingFooter ? null : (
        <footer
          className="fork-shell__footer"
          data-fork-region="footer"
          style={isMobile ? { ...footerStyle, flexShrink: 0 } : footerStyle}
        >
          {footer}
        </footer>
      )}
    </div>
  );
}

import React, { useState, useEffect } from "react";
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
  // 液态玻璃主题要求内容从输入栏玻璃下方滚过，因此把 footer 移进主区浮起来。
  // 移动端不这么做：那里 footer 要参与 flex 布局给软键盘让位。
  const floatingFooter = themeId === "glass" && !isMobile;
  // physical* 提前到 fallback 分支之前计算：下面的逻辑依赖它们，
  // 而 hooks 必须在条件返回之前无条件调用。
  const physicalLeftOpen = sidebarsSwapped ? rightOpen : leftOpen;
  const physicalRightOpen = sidebarsSwapped ? leftOpen : rightOpen;

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

  const mobileSidebarStyle = (side: "left" | "right"): React.CSSProperties => ({
    position: "fixed",
    top: "var(--mindfs-safe-area-top, env(safe-area-inset-top, 0px))",
    bottom: 0,
    [side]: 0,
    width: "var(--fork-mobile-drawer-width, 75vw)",
    zIndex: 2000,
    background: "var(--fork-mobile-drawer-bg, var(--mindfs-topbar-bg, var(--mobile-sidebar-bg, var(--sidebar-bg))))",
    boxShadow: side === "left" ? "4px 0 24px rgba(0,0,0,0.15)" : "-4px 0 24px rgba(0,0,0,0.15)",
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
    transform: "translateX(0) translateZ(0)",
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
        physicalLeftOpen && physicalLeftContent ? (
          <aside
            className="fork-shell__pane fork-shell__pane--left"
            data-fork-region="left"
            data-fork-open=""
            style={mobileSidebarStyle("left")}
          >
            {physicalLeftContent}
          </aside>
        ) : null
      ) : physicalLeftContent ? (
        <aside
          className="fork-shell__pane fork-shell__pane--left"
          data-fork-region="left"
          data-fork-open={physicalLeftOpen ? "" : undefined}
          style={desktopPaneStyle("left")}
        >
          {physicalLeftContent}
        </aside>
      ) : null}

      <main
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
        physicalRightOpen && physicalRightContent ? (
          <aside
            className="fork-shell__pane fork-shell__pane--right"
            data-fork-region="right"
            data-fork-open=""
            style={mobileSidebarStyle("right")}
          >
            {physicalRightContent}
          </aside>
        ) : null
      ) : physicalRightContent ? (
        <aside
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

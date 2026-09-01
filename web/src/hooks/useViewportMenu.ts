import { useLayoutEffect, useState, type RefObject } from "react";

type UseViewportMenuOptions = {
  open: boolean;
  /** 触发按钮/容器的 ref（用于取锚点矩形） */
  anchorRef: RefObject<HTMLElement | null>;
  /** 弹层节点的 ref（用于测量尺寸，须在 open 时挂载） */
  menuRef: RefObject<HTMLElement | null>;
  /** 弹出方向：top=向上弹出（按钮上方），bottom=向下弹出 */
  menuPlacement?: "top" | "bottom";
  /** 水平锚点：left=菜单左缘对齐按钮左缘，right=菜单右缘对齐按钮右缘 */
  align?: "left" | "right";
  /** 按钮与弹层的间距，默认 8px */
  gap?: number;
};

/**
 * 把「锚定在按钮上、position:absolute」的弹层改造成脱离容器的 fixed 定位。
 *
 * 背景：ForkShell 的 main 区域有 `overflow:hidden` + `contain:layout paint`，
 * 输入框周围按钮（模式选择 / Agent / shell 等）的弹层向上/向左展开时会被
 * main 边界硬裁剪（AgentSelector 的 viewportMenu 就是这个模式的先例）。
 * 本 hook 负责测量锚点与弹层尺寸，计算钳制在视觉视口内的 fixed 坐标，
 * 配合调用方 `createPortal(…, document.body)` 一起使用。
 *
 * 返回 null 表示尚未测量出位置（此时弹层应设 `visibility:hidden` 防止闪烁）。
 */
export function useViewportMenu({
  open,
  anchorRef,
  menuRef,
  menuPlacement = "top",
  align = "right",
  gap = 8,
}: UseViewportMenuOptions): { top: number; left: number } | null {
  const [position, setPosition] = useState<{ top: number; left: number } | null>(
    null,
  );

  useLayoutEffect(() => {
    if (!open || !anchorRef.current || !menuRef.current) {
      setPosition(null);
      return;
    }

    const anchor = anchorRef.current.getBoundingClientRect();
    const menu = menuRef.current.getBoundingClientRect();

    const viewport = window.visualViewport;
    const viewportLeft = viewport?.offsetLeft ?? 0;
    const viewportTop = viewport?.offsetTop ?? 0;
    const viewportWidth = viewport?.width ?? window.innerWidth;
    const viewportHeight = viewport?.height ?? window.innerHeight;
    const margin = 8;

    // 水平：右对齐时菜单右缘对齐按钮右缘，但钳制在视口内
    const maxLeft = viewportLeft + viewportWidth - menu.width - margin;
    const desiredLeft =
      align === "right" ? anchor.right - menu.width : anchor.left;
    const left = Math.max(
      viewportLeft + margin,
      Math.min(desiredLeft, maxLeft),
    );

    // 垂直：优先按 menuPlacement 展开；放不下则翻转到另一侧并钳制
    const below = anchor.bottom + gap;
    const above = anchor.top - menu.height - gap;
    const top =
      menuPlacement === "bottom" &&
      below + menu.height <= viewportTop + viewportHeight - margin
        ? below
        : Math.max(viewportTop + margin, above);

    setPosition((current) =>
      current &&
      Math.abs(current.top - top) < 0.5 &&
      Math.abs(current.left - left) < 0.5
        ? current
        : { top, left },
    );
  }, [open, anchorRef, menuRef, menuPlacement, align, gap]);

  return position;
}

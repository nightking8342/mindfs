import React from "react";

/**
 * 侧栏底部操作条 —— 一行圆形图标按钮。
 *
 * 上游把「从公网访问 / 回到节点页 / 安装应用」渲染成三个 width:100% 的大按钮，
 * 纵向堆叠，在侧栏底部占掉很大一块。这里收成一行 icon-only 的圆钮：
 * 文案降级为 title/aria-label，视觉重量让位给文件树本身。
 *
 * 组件放在 fork 独有文件里，上游 FileTree.tsx 只留一行调用，
 * 合并上游时冲突面积最小。
 */

export type ForkSidebarAction = {
  key: string;
  label: string;
  icon: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  /** 主操作用实心强调色，其余用描边 */
  primary?: boolean;
};

const SIZE = 34;

export function ForkSidebarActions({ actions }: { actions: ForkSidebarAction[] }) {
  const items = actions.filter(Boolean);
  if (items.length === 0) {
    return null;
  }
  return (
    <div
      data-fork-sidebar-actions=""
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "flex-start",
        gap: "8px",
        width: "100%",
        flexWrap: "wrap",
      }}
    >
      {items.map((action) => (
        <button
          key={action.key}
          type="button"
          title={action.label}
          aria-label={action.label}
          disabled={action.disabled}
          onClick={action.onClick}
          style={{
            width: `${SIZE}px`,
            height: `${SIZE}px`,
            flex: "0 0 auto",
            borderRadius: "50%",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            cursor: action.disabled ? "not-allowed" : "pointer",
            transition: "background 0.15s ease, border-color 0.15s ease, opacity 0.15s ease",
            opacity: action.disabled ? 0.45 : 1,
            border: action.primary
              ? "1px solid color-mix(in srgb, var(--accent-color) 72%, var(--border-color))"
              : "1px solid var(--border-color)",
            background: action.primary ? "var(--accent-color)" : "transparent",
            color: action.primary ? "#fff" : "var(--text-secondary)",
            padding: 0,
          }}
        >
          {action.icon}
        </button>
      ))}
    </div>
  );
}

const iconProps = {
  width: 15,
  height: 15,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
  "aria-hidden": true,
};

/** 从公网访问 */
export const ForkGlobeIcon = (
  <svg {...iconProps}>
    <circle cx="12" cy="12" r="9" />
    <path d="M3 12h18" />
    <path d="M12 3a14 14 0 0 1 3.6 9A14 14 0 0 1 12 21a14 14 0 0 1-3.6-9A14 14 0 0 1 12 3z" />
  </svg>
);

/** 回到节点页 */
export const ForkHomeIcon = (
  <svg {...iconProps}>
    <path d="M3 10.5 12 3l9 7.5" />
    <path d="M5 9.8V19a1.6 1.6 0 0 0 1.6 1.6h10.8A1.6 1.6 0 0 0 19 19V9.8" />
    <path d="M9.6 20.6v-6h4.8v6" />
  </svg>
);

/** 安装应用 */
export const ForkInstallIcon = (
  <svg {...iconProps}>
    <path d="M12 16V4" />
    <path d="m7 9 5-5 5 5" />
    <path d="M20 16.5v1.5A2 2 0 0 1 18 20H6a2 2 0 0 1-2-2v-1.5" />
  </svg>
);

import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useI18n } from "../i18n";
import { useViewportMenu } from "../hooks/useViewportMenu";

/**
 * One selectable repository. An empty path means the managed root itself.
 */
export type WorktreeRepoOption = {
  path: string;
  label: string;
  branch?: string;
};

type WorktreeRepoSelectorProps = {
  options: WorktreeRepoOption[];
  /** Currently selected repository path; empty selects the managed root. */
  value: string;
  /** Renders the placeholder label until the user picks a repository. */
  unselected?: boolean;
  disabled?: boolean;
  /** Opens the menu on mount, used when no repository is preselected. */
  autoOpen?: boolean;
  height?: number;
  maxWidth?: number;
  menuAlign?: "left" | "right";
  menuPlacement?: "top" | "bottom";
  onChange: (repoPath: string) => void;
};

/**
 * Repository picker shown next to the worktree toggle when a managed root holds
 * more than one repository. Deliberately mirrors WorktreeBranchSelector's shape
 * (same sizing, menu placement and dismissal behaviour) so the two read as one
 * control strip; kept as a separate component to avoid touching the upstream
 * branch selector.
 */
export function WorktreeRepoSelector({
  options,
  value,
  unselected = false,
  disabled = false,
  autoOpen = false,
  height = 24,
  maxWidth = 200,
  menuAlign = "right",
  menuPlacement = "bottom",
  onChange,
}: WorktreeRepoSelectorProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const viewportMenuPos = useViewportMenu({
    open,
    anchorRef: containerRef,
    menuRef,
    menuPlacement,
    align: menuAlign,
  });
  const autoOpenedRef = useRef(false);
  const selected = options.find((item) => item.path === value);
  const label = unselected || !selected
    ? t("worktree.selectRepository")
    : selected.label;

  // Opening on mount is how "no repository preselected" is surfaced: picking the
  // wrong repository sends the agent to work in the wrong place, so the choice is
  // put in front of the user instead of blocking the send button. Only ever fires
  // once, so a manual close is not undone.
  useEffect(() => {
    if (!autoOpen || autoOpenedRef.current || disabled) {
      return;
    }
    autoOpenedRef.current = true;
    setOpen(true);
  }, [autoOpen, disabled]);

  useEffect(() => {
    if (disabled) {
      setOpen(false);
    }
  }, [disabled]);

  useEffect(() => {
    if (!open) return;

    const handlePointerOutside = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };

    document.addEventListener("pointerdown", handlePointerOutside);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerOutside);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open]);

  const selectRepo = (repoPath: string) => {
    onChange(repoPath);
    setOpen(false);
  };

  const renderCheck = (isSelected: boolean) => (
    <span
      aria-hidden="true"
      style={{
        width: "18px",
        height: "18px",
        flex: "0 0 18px",
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        color: isSelected ? "var(--accent-color)" : "transparent",
      }}
    >
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
        <path d="m5 12.5 4.1 4L19 7" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </span>
  );

  return (
    <div ref={containerRef} style={{ position: "relative", minWidth: 0, maxWidth: "100%" }}>
      <button
        type="button"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t("worktree.selectRepository")}
        title={unselected || !selected ? t("worktree.selectRepository") : selected.path || selected.label}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            setOpen(true);
          }
        }}
        style={{
          width: "100%",
          minWidth: "84px",
          maxWidth: `${maxWidth}px`,
          height: `${height}px`,
          borderRadius: "6px",
          border: open
            ? "1px solid var(--accent-color)"
            : unselected
              ? "1px solid rgba(180, 83, 9, 0.42)"
              : "1px solid var(--border-color)",
          background: "var(--mobile-overlay-bg)",
          color: unselected ? "#b45309" : "var(--text-primary)",
          display: "flex",
          alignItems: "center",
          gap: "5px",
          padding: "0 7px 0 9px",
          outline: "none",
          cursor: disabled ? "not-allowed" : "pointer",
          opacity: disabled ? 0.72 : 1,
          boxShadow: open ? "0 0 0 2px color-mix(in srgb, var(--accent-color) 14%, transparent)" : "none",
        }}
      >
        <span
          aria-hidden="true"
          style={{ flex: "0 0 auto", display: "inline-flex", alignItems: "center", color: "var(--text-secondary)" }}
        >
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none">
            <path d="M4 6a2 2 0 0 1 2-2h4l2 2h6a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </span>
        <span
          style={{
            minWidth: 0,
            flex: 1,
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
            textAlign: "left",
            fontSize: height > 24 ? "12px" : "11px",
            fontWeight: 700,
          }}
        >
          {label}
        </span>
        <svg
          width="13"
          height="13"
          viewBox="0 0 24 24"
          fill="none"
          aria-hidden="true"
          style={{
            flex: "0 0 auto",
            color: "var(--text-secondary)",
            transform: open ? "rotate(180deg)" : "rotate(0deg)",
            transition: "transform 160ms ease",
          }}
        >
          <path d="m7 10 5 5 5-5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </button>

      {open ? createPortal(
        <div
          ref={menuRef}
          role="listbox"
          aria-label={t("worktree.selectRepository")}
          style={{
            position: "fixed",
            top: viewportMenuPos?.top ?? 0,
            left: viewportMenuPos?.left ?? 0,
            visibility: viewportMenuPos ? "visible" : "hidden",
            zIndex: 1200,
            width: "max-content",
            minWidth: "220px",
            maxWidth: "min(320px, calc(100vw - 24px))",
            maxHeight: "min(46dvh, 292px)",
            overflowY: "auto",
            overscrollBehavior: "contain",
            padding: "6px",
            border: "1px solid var(--menu-border)",
            borderRadius: "12px",
            background: "var(--menu-bg)",
            boxShadow: "0 14px 36px rgba(15, 23, 42, 0.18)",
          }}
        >
          {options.map((item) => {
            const isSelected = !unselected && item.path === value;
            return (
              <button
                key={item.path || "__root__"}
                type="button"
                role="option"
                aria-selected={isSelected}
                title={item.path || item.label}
                onClick={() => selectRepo(item.path)}
                style={{
                  width: "100%",
                  minHeight: "38px",
                  padding: "7px 9px",
                  border: "none",
                  borderRadius: "8px",
                  background: isSelected ? "rgba(59, 130, 246, 0.10)" : "transparent",
                  color: isSelected ? "var(--accent-color)" : "var(--text-primary)",
                  display: "flex",
                  alignItems: "center",
                  gap: "7px",
                  textAlign: "left",
                  cursor: "pointer",
                }}
              >
                <span style={{ minWidth: 0, flex: 1, display: "flex", flexDirection: "column", gap: "1px" }}>
                  <span
                    style={{
                      fontSize: "13px",
                      fontWeight: 700,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {item.label}
                  </span>
                  {item.branch ? (
                    <span
                      style={{
                        fontSize: "11px",
                        color: "var(--text-secondary)",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {item.branch}
                    </span>
                  ) : null}
                </span>
                {renderCheck(isSelected)}
              </button>
            );
          })}
        </div>,
        document.body,
      ) : null}
    </div>
  );
}

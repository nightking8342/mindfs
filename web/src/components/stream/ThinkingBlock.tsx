import React, { memo, useEffect, useState } from "react";
import { useI18n } from "../../i18n";

type ThinkingBlockProps = {
  content: string;
  defaultExpanded?: boolean;
  onExpandedChange?: (expanded: boolean) => void;
};

export const ThinkingBlock = memo(function ThinkingBlock({ content, defaultExpanded = false, onExpandedChange }: ThinkingBlockProps) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(defaultExpanded);
  useEffect(() => {
    if (!defaultExpanded) {
      setExpanded(false);
    }
  }, [defaultExpanded]);

  if (!content) return null;

  return (
    <div
      style={{
        borderRadius: "8px",
        border: "1px solid var(--border-color)",
        background: "var(--content-bg)",
        overflow: "hidden",
      }}
    >
      <button
        type="button"
        onClick={() => {
          onExpandedChange?.(!expanded);
          setExpanded(!expanded);
        }}
        style={{
          width: "100%",
          display: "flex",
          alignItems: "center",
          gap: "5px",
          justifyContent: "space-between",
          padding: "6px 8px",
          background: "none",
          border: "none",
          cursor: "pointer",
          fontSize: "12px",
          color: "#8b5cf6",
          fontWeight: 500,
          whiteSpace: "nowrap",
          overflow: "hidden",
        }}
      >
        <span style={{ display: "inline-flex", alignItems: "center", gap: "5px", minWidth: 0, flex: 1 }}>
          <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{t("thinking.title")}</span>
          <span style={{ color: "var(--text-secondary)", fontWeight: 400, flexShrink: 0 }}>
            ({t("common.characterCount", { count: content.length })})
          </span>
        </span>
        <span
          style={{
            flexShrink: 0,
            transform: expanded ? "rotate(90deg)" : "rotate(0deg)",
            transition: "transform 0.2s",
            color: "var(--text-secondary)",
            display: "inline-flex",
            alignItems: "center",
          }}
        >
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.25" strokeLinecap="round" strokeLinejoin="round">
            <polyline points="9 18 15 12 9 6" />
          </svg>
        </span>
      </button>

      {expanded && (
        <div
          style={{
            padding: "0 10px 10px",
            fontSize: "12px",
            lineHeight: 1.5,
            color: "var(--text-secondary)",
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
            maxHeight: "200px",
            overflow: "auto",
          }}
        >
          {content}
        </div>
      )}
    </div>
  );
});

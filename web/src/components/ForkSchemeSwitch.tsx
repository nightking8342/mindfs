import React, { useEffect, useState } from "react";
import { useI18n } from "../i18n";
import type { AppearanceMode } from "../services/appearance";
import {
  SCHEME_CHANGE_EVENT,
  fixedSchemeOf,
  getSchemePref,
  setSchemePref,
  themeSupportsBothSchemes,
  type SchemePref,
} from "../services/forkScheme";

/**
 * 明暗开关 —— 与主题正交的第二个维度。
 *
 * 上游把「跟随系统」当成主题列表里的一项，导致「某主题 + 跟随系统」无法表达。
 * 这里把明暗独立出来：主题列表只管配色，明暗由这三档控制。
 *
 * 组件放在 fork 独有文件里，上游 FileTree.tsx 只留一行调用，
 * 合并上游时冲突面积最小。
 */

const OPTIONS: Array<{ value: SchemePref; labelKey: "appearance.scheme.auto" | "appearance.scheme.light" | "appearance.scheme.dark" }> = [
  { value: "auto", labelKey: "appearance.scheme.auto" },
  { value: "light", labelKey: "appearance.scheme.light" },
  { value: "dark", labelKey: "appearance.scheme.dark" },
];

export function ForkSchemeSwitch({ appearanceMode }: { appearanceMode: AppearanceMode }) {
  const { t } = useI18n();
  const [pref, setPref] = useState<SchemePref>(() => getSchemePref());

  useEffect(() => {
    const sync = () => setPref(getSchemePref());
    window.addEventListener(SCHEME_CHANGE_EVENT, sync);
    return () => window.removeEventListener(SCHEME_CHANGE_EVENT, sync);
  }, []);

  // 单模式主题切了也没用，置灰比让它能点但无反应更诚实——后者会被当成 bug
  const enabled = themeSupportsBothSchemes(appearanceMode);
  const locked = fixedSchemeOf(appearanceMode);

  return (
    <div style={{ padding: "6px 10px 8px" }}>
      <div
        style={{
          fontSize: "11px",
          color: "var(--text-secondary)",
          marginBottom: "6px",
          display: "flex",
          alignItems: "center",
          gap: "6px",
        }}
      >
        <span>{t("appearance.scheme")}</span>
        {!enabled && locked ? (
          <span style={{ opacity: 0.75 }}>
            · {t(locked === "dark" ? "appearance.scheme.lockedDark" : "appearance.scheme.lockedLight")}
          </span>
        ) : null}
      </div>
      <div
        style={{
          display: "flex",
          gap: "2px",
          padding: "2px",
          borderRadius: "8px",
          background: "var(--menu-active-bg)",
          opacity: enabled ? 1 : 0.45,
        }}
      >
        {OPTIONS.map((option) => {
          const active = enabled && option.value === pref;
          return (
            <button
              key={option.value}
              type="button"
              disabled={!enabled}
              onClick={() => {
                setSchemePref(option.value);
                setPref(option.value);
              }}
              style={{
                flex: 1,
                border: "none",
                borderRadius: "6px",
                padding: "4px 6px",
                fontSize: "11px",
                cursor: enabled ? "pointer" : "default",
                background: active ? "var(--panel-bg)" : "transparent",
                color: active ? "var(--accent-color)" : "var(--text-primary)",
                fontWeight: active ? 600 : 400,
                boxShadow: active ? "var(--panel-shadow)" : "none",
              }}
            >
              {t(option.labelKey)}
            </button>
          );
        })}
      </div>
    </div>
  );
}

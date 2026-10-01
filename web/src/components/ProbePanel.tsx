import { useCallback, useEffect, useState } from "react";
// 临时诊断面板（pending 探针），排查结束后连同 services/pendingProbe.ts 一起删除。
import {
  defaultProbeEndpoint,
  downloadProbeBundle,
  pendingProbeEnabled,
  probeAggregates,
  probeSuspiciousRecords,
  sendProbeBundle,
  setProbeEndpoint,
  setPendingProbeEnabled,
} from "../services/pendingProbe";

const ENDPOINT_KEY = "mindfs-pending-probe-endpoint";

/**
 * 悬浮诊断面板。刻意做得很小、很丑、固定右下角：
 * 它的存在周期只有一次排查，不值得占用任何设计预算。
 */
export function ProbePanel() {
  const [open, setOpen] = useState(false);
  const [enabled, setEnabled] = useState(() => pendingProbeEnabled());
  const [endpoint, setEndpoint] = useState(() => defaultProbeEndpoint());
  const [status, setStatus] = useState("");
  const [tick, setTick] = useState(0);

  useEffect(() => {
    setEnabled(pendingProbeEnabled());
  }, []);

  const toggle = useCallback(() => {
    const next = !pendingProbeEnabled();
    setPendingProbeEnabled(next);
    setEnabled(next);
    setStatus(next ? "已开启（需刷新页面后开始记录）" : "已关闭");
  }, []);

  const readAggregates = useCallback(() => probeAggregates(), []);
  const readSuspicious = useCallback(() => probeSuspiciousRecords(), []);

  const handleSend = useCallback(async () => {
    setStatus("正在上报…");
    setProbeEndpoint(endpoint.trim());
    const result = await sendProbeBundle(endpoint.trim() || defaultProbeEndpoint());
    setStatus(result.ok ? `已上报：${result.detail}` : `失败：${result.detail}`);
    setTick((v) => v + 1);
  }, [endpoint]);

  const handleDownload = useCallback(async () => {
    const result = await downloadProbeBundle();
    setStatus(result.ok ? "已触发下载（见浏览器下载目录）" : `失败：${result.detail}`);
  }, []);

  const aggregates = readAggregates();
  const suspiciousCount = readSuspicious().length;
  void tick;

  const buttonStyle: React.CSSProperties = {
    border: "none",
    borderRadius: "6px",
    padding: "6px 10px",
    fontSize: "12px",
    cursor: "pointer",
    background: "var(--accent-color, #2563eb)",
    color: "#fff",
    flex: "1 1 auto",
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        style={{
          position: "fixed",
          right: "10px",
          bottom: "10px",
          zIndex: 2147483000,
          width: "40px",
          height: "40px",
          borderRadius: "20px",
          border: "none",
          background: enabled ? "rgba(220,38,38,0.92)" : "rgba(37,99,235,0.92)",
          color: "#fff",
          fontSize: "11px",
          fontWeight: 700,
          cursor: "pointer",
          boxShadow: "0 6px 20px rgba(15,23,42,0.35)",
        }}
      >
        探针
      </button>
    );
  }

  return (
    <div
      style={{
        position: "fixed",
        right: "10px",
        bottom: "10px",
        zIndex: 2147483000,
        width: "min(320px, calc(100vw - 20px))",
        maxHeight: "70vh",
        overflowY: "auto",
        padding: "10px",
        borderRadius: "10px",
        background: "var(--panel-bg, #ffffff)",
        color: "var(--text-primary, #0f172a)",
        border: "1px solid var(--border-color, #cbd5e1)",
        boxShadow: "0 10px 30px rgba(15,23,42,0.35)",
        fontSize: "12px",
        lineHeight: 1.5,
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <strong>会话状态探针</strong>
        <button
          type="button"
          onClick={() => setOpen(false)}
          style={{
            border: "none",
            background: "transparent",
            color: "inherit",
            fontSize: "16px",
            cursor: "pointer",
            padding: "0 4px",
          }}
        >
          ×
        </button>
      </div>

      <div style={{ marginTop: "6px", opacity: 0.85 }}>
        状态：{enabled ? "记录中" : "未启用"} · 异常 {suspiciousCount}
      </div>

      <div style={{ marginTop: "8px", display: "flex", gap: "6px", flexWrap: "wrap" }}>
        <button type="button" onClick={toggle} style={buttonStyle}>
          {enabled ? "关闭探针" : "开启探针"}
        </button>
        <button
          type="button"
          onClick={handleSend}
          style={{ ...buttonStyle, background: "#059669" }}
        >
          上报到电脑
        </button>
      </div>

      <div style={{ marginTop: "6px", display: "flex", gap: "6px" }}>
        <button
          type="button"
          onClick={handleDownload}
          style={{ ...buttonStyle, background: "var(--text-secondary, #64748b)" }}
        >
          下载文件
        </button>
        <button
          type="button"
          onClick={() => {
            setTick((v) => v + 1);
            setStatus("已刷新计数");
          }}
          style={{ ...buttonStyle, background: "var(--text-secondary, #64748b)" }}
        >
          刷新
        </button>
      </div>

      <label style={{ display: "block", marginTop: "8px" }}>
        <span style={{ opacity: 0.85 }}>上报地址</span>
        <input
          value={endpoint}
          onChange={(event) => {
            setEndpoint(event.target.value);
            setProbeEndpoint("");
            try {
              window.localStorage.setItem(ENDPOINT_KEY, event.target.value);
            } catch {
              // 忽略。
            }
          }}
          style={{
            width: "100%",
            boxSizing: "border-box",
            marginTop: "2px",
            padding: "4px 6px",
            fontSize: "11px",
            borderRadius: "6px",
            border: "1px solid var(--border-color, #cbd5e1)",
            background: "var(--input-bg, transparent)",
            color: "inherit",
          }}
        />
      </label>

      {status ? (
        <div style={{ marginTop: "6px", wordBreak: "break-all", opacity: 0.9 }}>{status}</div>
      ) : null}

      {aggregates.length > 0 ? (
        <table
          style={{
            marginTop: "8px",
            width: "100%",
            borderCollapse: "collapse",
            fontSize: "11px",
          }}
        >
          <thead>
            <tr style={{ opacity: 0.7 }}>
              <th style={{ textAlign: "left" }}>判定</th>
              <th style={{ textAlign: "right" }}>总</th>
              <th style={{ textAlign: "right" }}>异常</th>
            </tr>
          </thead>
          <tbody>
            {aggregates.map((row) => (
              <tr key={String(row.tag)}>
                <td style={{ wordBreak: "break-all" }}>{String(row.tag)}</td>
                <td style={{ textAlign: "right" }}>{String(row.total)}</td>
                <td
                  style={{
                    textAlign: "right",
                    color: Number(row.suspicious) > 0 ? "#dc2626" : "inherit",
                    fontWeight: Number(row.suspicious) > 0 ? 700 : 400,
                  }}
                >
                  {String(row.suspicious)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div style={{ marginTop: "8px", opacity: 0.6 }}>暂无记录</div>
      )}
    </div>
  );
}

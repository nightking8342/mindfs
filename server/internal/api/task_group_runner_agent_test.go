package api

import (
	"testing"

	"mindfs/server/internal/session"
)

// TestGroupTurnAgentResolution 固化 fork 的修复：RunGroupTurn 不得使用硬编码的
// "codex" 回退值。
//
// 背景：该回退值会与同一次调用里的 sess.Model 一起去 validateAgentModel 校验，
// 非 codex 用户（claude + opus）必然失败 → 任务组被记成组级 blocked → 整个
// 任务组冻结且 block_reason 不会自动清除（实测于 vFlow 项目）。
//
// 修复后 agent 取自 AgentCtxSeq（由 session_agent_bindings 表填充，来源可靠），
// 会话历史仅作兜底，两者都取不到时留空（validateAgentModel 对空 agent 直接放行）。
//
// 本测试覆盖纯函数部分的选择逻辑，不依赖 AppContext 的完整装配。
func TestGroupTurnAgentResolution(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ctxSeq     map[string]int
		exchanges  []session.Exchange
		wantAgent  string
		wantReason string
	}{
		{
			name:       "AgentCtxSeq 优先且取 seq 最大者",
			ctxSeq:     map[string]int{"claude": 26},
			wantAgent:  "claude",
			wantReason: "vFlow 实际场景：bindings 表记录 claude，不得回退 codex",
		},
		{
			name:       "多 agent 时取最近驱动者（seq 最大）",
			ctxSeq:     map[string]int{"codex": 3, "claude": 26},
			wantAgent:  "claude",
			wantReason: "换过 agent 的会话，应选最近用过的",
		},
		{
			name:       "AgentCtxSeq 为空时回落会话历史",
			ctxSeq:     nil,
			exchanges:  []session.Exchange{{Agent: "gemini"}, {Agent: "claude"}},
			wantAgent:  "claude",
			wantReason: "兜底路径取历史最后一条（沿用旧语义）",
		},
		{
			name:       "两者皆空时留空而非 codex",
			ctxSeq:     nil,
			exchanges:  nil,
			wantAgent:  "",
			wantReason: "留空可让校验放行；硬编码 codex 会导致必然失败",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveGroupTurnAgent(tc.ctxSeq, tc.exchanges)
			if got != tc.wantAgent {
				t.Errorf("agent = %q, 期望 %q（%s）", got, tc.wantAgent, tc.wantReason)
			}
			if got == "codex" && tc.wantAgent != "codex" {
				t.Error("回退到了硬编码的 codex —— 这正是本次修复要消除的行为")
			}
		})
	}
}

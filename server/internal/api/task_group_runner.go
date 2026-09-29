package api

import (
	"context"
	"errors"
	"strings"

	"mindfs/server/internal/api/usecase"
	"mindfs/server/internal/kanban"
	"mindfs/server/internal/session"
)

func (s *AppContext) ValidateGroupSession(ctx context.Context, root, key string) error {
	uc := &usecase.Service{Registry: s}
	sess, e := uc.GetSession(ctx, usecase.GetSessionInput{RootID: root, Key: key})
	if e != nil {
		return e
	}
	if sess == nil {
		return errors.New("parent session not found")
	}
	if sess.Type != "chat" {
		return errors.New("source must be a chat session")
	}
	return nil
}
func (s *AppContext) GroupSessionBusy(root, key string) bool {
	return usecase.SessionTurnActive(root, key)
}
func (s *AppContext) RunGroupTurn(ctx context.Context, g kanban.TaskGroup, prompt string) error {
	uc := &usecase.Service{Registry: s}
	sess, e := uc.GetSession(ctx, usecase.GetSessionInput{RootID: g.RootID, Key: g.SessionKey})
	if e != nil {
		return e
	}
	if sess == nil {
		return errors.New("parent session not found")
	}
	agentName := resolveGroupTurnAgent(sess.AgentCtxSeq, sess.Exchanges)
	return s.RunAgentStage(ctx, kanban.AgentStageExecution{RootID: g.RootID, Stage: kanban.StageTemplate{Agent: agentName, Model: sess.Model, PlanMode: sess.PlanMode}, Run: kanban.StageRun{SessionKey: g.SessionKey}, Prompt: prompt})
}
func (s *AppContext) GroupUpdated(g kanban.TaskGroup) {
	s.GetSessionStreamHub().BroadcastAll(WSResponse{Type: "task-group.updated", Payload: map[string]any{"root_id": g.RootID, "group": g}})
}

func (s *AppContext) DeleteSessionTaskGroups(ctx context.Context, root string, keys []string) ([]string, error) {
	svc, err := s.GetKanbanService()
	if err != nil {
		return nil, err
	}
	return svc.DeleteSessionGroups(ctx, root, keys)
}

// resolveGroupTurnAgent 决定唤醒父会话时该用哪个 agent。
//
// fork: 上游原实现是「从 exchanges 倒推 + 硬编码回退 "codex"」。该回退值会与
// 同一次调用里的 sess.Model 一起送去 validateAgentModel 校验（要求 model 属于
// 该 agent 的模型列表），因此对非 codex 用户（如 claude + opus）必然失败；失败
// 被 task_groups 记成组级 blocked，等于整个任务组冻结，且 block_reason 不会自动
// 清除（实测于 vFlow 项目，见 docs/task-orchestration-internals.md）。
//
// 取值优先级：
//  1. AgentCtxSeq —— 由 session_agent_bindings 表填充（session/manager.go 的
//     loadSessionUnsafe），来源可靠、与会话历史是否加载完整无关。同会话换过
//     agent 时取上下文序号最大者，即最近驱动过的那个。
//  2. 会话历史倒推 —— 沿用上游语义的兜底。
//  3. 空串 —— 两者都取不到时留空。validateAgentModel 对空 agent 直接放行，
//     比拿一个错误的 agent 去比更安全。
func resolveGroupTurnAgent(agentCtxSeq map[string]int, exchanges []session.Exchange) string {
	agentName := ""
	maxSeq := -1
	for name, seq := range agentCtxSeq {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seq > maxSeq {
			agentName, maxSeq = name, seq
		}
	}
	if agentName != "" {
		return agentName
	}
	for i := len(exchanges) - 1; i >= 0; i-- {
		if agent := strings.TrimSpace(exchanges[i].Agent); agent != "" {
			return agent
		}
	}
	return ""
}

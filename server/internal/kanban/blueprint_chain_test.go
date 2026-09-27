package kanban

import (
	"context"
	"strings"
	"testing"
)

// TestBlueprintTemplateChain 复现「蓝图」模板的完整阶段链，验证每一步的
// {previous_input} 实际取值，回答调研文档 §待确认清单 第 1 条：
//
//	需求(user) → 方案(agent, auto) → 审核(user) → 实现(agent, auto) → 验收(agent)
//
// 关键疑问：实现阶段的 {previous_input} 拿到的是「方案全文」还是「原始需求」？
func TestBlueprintTemplateChain(t *testing.T) {
	ctx := context.Background()
	s, store, p := orchestrationFixture(t)

	const (
		demandText = "DEMAND_原始需求"
		planText   = "PLAN_方案全文_MARKER"
		reviewText = "REVIEW_审核意见_MARKER"
		implText   = "IMPL_实现交付_MARKER"
	)

	// 构造蓝图式模板：user → agent(auto) → user → agent(auto) → agent
	tmpl := TaskTemplate{
		Name:           "blueprint-probe",
		MaxConcurrency: 2,
		Stages: []TaskTemplateStage{
			{Position: 0, Snapshot: StageTemplate{Name: "需求", Role: RoleUser}},
			{Position: 1, Snapshot: StageTemplate{
				Name: "方案", Role: RoleAgent, Agent: "codex",
				AutoAdvance: true, PromptTemplate: "design {previous_input}",
			}},
			{Position: 2, Snapshot: StageTemplate{Name: "审核", Role: RoleUser}},
			{Position: 3, Snapshot: StageTemplate{
				Name: "实现", Role: RoleAgent, Agent: "codex",
				AutoAdvance: true, PromptTemplate: "implement {previous_input}",
			}},
			{Position: 4, Snapshot: StageTemplate{
				Name: "验收", Role: RoleAgent, Agent: "codex",
				PromptTemplate: "verify {previous_input}",
			}},
		},
	}
	saved, err := s.Templates.SaveTaskTemplate(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateTask(ctx, CreateTaskInput{
		RootID: p.Task.RootID, GroupID: p.Task.GroupID,
		TaskTemplateID: saved.ID, Input: demandText,
	})
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, p)

	prompts := map[int]string{} // 各阶段实际渲染的 prompt
	s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
		prompts[exec.Run.StageIndex] = exec.Prompt
		switch exec.Run.StageIndex {
		case 1:
			// 方案阶段交付：写入 planText
			_, e := s.ManagedAction(ctx, p.Task.RootID, c.Task.ID, "from-task",
				ManagedInput{Completed: true, Message: planText})
			return e
		case 3:
			// 实现阶段交付
			_, e := s.ManagedAction(ctx, p.Task.RootID, c.Task.ID, "from-task",
				ManagedInput{Completed: true, Message: implText})
			return e
		}
		return nil
	}}
	s.running = map[string]bool{p.Task.RootID + "/" + c.Task.ID: true}

	drive := func() Task {
		t.Helper()
		cur, _ := store.GetTask(ctx, c.Task.ID)
		cur.Status = StatusRunning
		cur.SchedulerAdmitted = true
		if err := store.UpdateTask(ctx, cur); err != nil {
			t.Fatal(err)
		}
		if err := s.executeTask(ctx, p.Task.RootID, c.Task.ID); err != nil {
			t.Fatal(err)
		}
		got, _ := store.GetTask(ctx, c.Task.ID)
		t.Logf("  -> stage=%d status=%s", got.CurrentStageIndex, got.Status)
		return got
	}

	// 阶段 0 → 1：模拟用户填完需求点开始
	drive() // 进入方案阶段（agent 自动跑，交付 planText）
	drive() // 方案 auto 推进 → 审核阶段
	// 审核：用户写下审核意见
	if _, err := s.nextManaged(ctx, MoveInput{RootID: p.Task.RootID, TaskID: c.Task.ID, Reason: reviewText}); err != nil {
		t.Fatalf("审核推进失败: %v", err)
	}
	drive() // 实现阶段（agent 自动跑，交付 implText）
	drive() // 实现 auto 推进 → 验收阶段

	t.Log("=== 各阶段渲染的 prompt ===")
	for i := 0; i <= 4; i++ {
		if pr, ok := prompts[i]; ok {
			t.Logf("阶段 %d: %s", i, strings.ReplaceAll(pr, "\n", " ⏎ "))
		}
	}

	// —— 核心断言 ——
	// 方案阶段应看到原始需求
	if pr := prompts[1]; !strings.Contains(pr, demandText) {
		t.Errorf("方案阶段未拿到原始需求: %q", pr)
	}
	// 关键：实现阶段拿到什么？
	implPrompt := prompts[3]
	t.Logf("\n【核心问题】实现阶段的 {previous_input} 实际内容：")
	switch {
	case strings.Contains(implPrompt, planText):
		t.Log("  → 拿到【方案全文】✅ 符合预期")
	case strings.Contains(implPrompt, reviewText):
		t.Log("  → 拿到【审核意见】")
	case strings.Contains(implPrompt, demandText):
		t.Log("  → 回落到【原始需求】❌ 方案丢失")
	default:
		t.Logf("  → 其他: %q", implPrompt)
	}
}

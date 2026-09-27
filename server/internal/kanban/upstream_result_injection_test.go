package kanban

import (
	"context"
	"strings"
	"testing"
)

// TestUpstreamResultReachesDownstreamPrompt 验证任务组的核心承诺：
// 上游任务的交付内容（run.Result）会被注入下游任务的 prompt，
// 使下游无需依赖会话历史就能读到上游产出。
func TestUpstreamResultReachesDownstreamPrompt(t *testing.T) {
	ctx := context.Background()
	s, store, g := groupFixture(t)

	const upstreamResult = "UPSTREAM_DELIVERABLE_MARKER_42"
	a := groupTask(t, s, g, "produce the plan")
	b := groupTask(t, s, g, "consume the plan", a.Task.ID)

	graph, _ := s.GroupGraph(ctx, g.RootID, g.ID)
	if _, e := s.GroupAction(ctx, g.RootID, g.ID, "publish", ManagedInput{PlanVersion: &graph.Group.PlanVersion}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.GroupAction(ctx, g.RootID, g.ID, "approve-plan", ManagedInput{PlanVersion: &graph.Group.PlanVersion}); e != nil {
		t.Fatal(e)
	}

	// 任务 A 交付：把标记文本写进 run.Result
	ra := &orchestrationRunner{run: func(exec AgentStageExecution) error {
		_, e := s.ManagedAction(ctx, g.RootID, a.Task.ID, "from-task",
			ManagedInput{Completed: true, Message: upstreamResult})
		return e
	}}
	s.Runner = ra
	freshA, _ := store.GetTask(ctx, a.Task.ID)
	freshA.Status = StatusRunning
	freshA.SchedulerAdmitted = true
	_ = store.UpdateTask(ctx, freshA)
	s.running = map[string]bool{g.RootID + "/" + a.Task.ID: true}
	if e := s.executeTask(ctx, g.RootID, a.Task.ID); e != nil {
		t.Fatal(e)
	}

	// 确认 A 的结果确实落盘了
	da, _ := store.GetDetail(ctx, a.Task.ID)
	foundResult := false
	for _, r := range da.StageRuns {
		if strings.Contains(r.Result, upstreamResult) {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatalf("上游结果未落盘，StageRuns=%+v", da.StageRuns)
	}

	// 任务 B 启动时捕获它的 prompt
	var captured string
	s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
		captured = exec.Prompt
		return nil
	}}
	freshB, _ := store.GetTask(ctx, b.Task.ID)
	if !s.managedReady(ctx, store, freshB) {
		t.Fatal("下游任务在依赖完成后仍未就绪")
	}
	freshB.Status = StatusRunning
	freshB.SchedulerAdmitted = true
	_ = store.UpdateTask(ctx, freshB)
	s.running = map[string]bool{g.RootID + "/" + b.Task.ID: true}
	if e := s.executeTask(ctx, g.RootID, b.Task.ID); e != nil {
		t.Fatal(e)
	}

	if captured == "" {
		t.Fatal("未捕获到下游 prompt")
	}
	t.Logf("下游 prompt 全文:\n%s", captured)

	if !strings.Contains(captured, upstreamResult) {
		t.Error("上游交付内容未注入下游 prompt —— 任务组无法传递结果")
	}
	if !strings.Contains(captured, "## 前置任务") {
		t.Error("缺少「## 前置任务」区块")
	}
}

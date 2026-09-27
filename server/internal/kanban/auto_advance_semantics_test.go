package kanban

import (
	"context"
	"testing"
)

// TestAutoAdvanceOnAgentStageSkipsWaitingUser 验证 auto_advance 的确切语义。
// 它作用于「刚完成的那个阶段」：
//   - true  → agent 交付后自动推进到下一阶段（该阶段不产生人工关卡）
//   - false → 停下，任务进入 waiting_user 等用户推进
func TestAutoAdvanceOnAgentStageSkipsWaitingUser(t *testing.T) {
	for _, tc := range []struct {
		name        string
		autoAdvance bool
		wantStage   int
		wantStatus  string
	}{
		// 自动推进后进入阶段 2；新阶段尚未被调度器 admit，故状态为 queued。
		// 关键断言是阶段索引前进了——无需用户介入。
		{"自动推进", true, 2, StatusQueued},
		{"停下等用户", false, 1, StatusWaitingUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, store, p := orchestrationFixture(t)

			// 基于父模板造一个三阶段模板：user(0) -> agent(1) -> agent(2)
			tmpl, _ := s.Templates.GetTaskTemplate(p.Task.TaskTemplateID)
			tmpl.ID = ""
			tmpl.Name = "auto-advance-probe"
			tmpl.Stages[1].Snapshot.AutoAdvance = tc.autoAdvance
			tmpl.Stages = append(tmpl.Stages, TaskTemplateStage{
				Position: 2,
				Snapshot: StageTemplate{
					Name: "验收", Role: RoleAgent, Agent: "codex",
					PromptTemplate: "review {previous_input}",
				},
			})
			saved, err := s.Templates.SaveTaskTemplate(tmpl)
			if err != nil {
				t.Fatal(err)
			}

			// 该模板建一个组内子任务（组因此非空，可发布）
			c, err := s.CreateTask(ctx, CreateTaskInput{
				RootID: p.Task.RootID, GroupID: p.Task.GroupID,
				TaskTemplateID: saved.ID, Input: "two agent stages",
			})
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, p)

			// 阶段 1 交付后观察落点
			s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
				if exec.Run.StageIndex == 1 {
					_, e := s.ManagedAction(ctx, p.Task.RootID, c.Task.ID, "from-task",
						ManagedInput{Completed: true, Message: "stage one done"})
					return e
				}
				return nil
			}}
			cur, _ := store.GetTask(ctx, c.Task.ID)
			cur.Status = StatusRunning
			cur.SchedulerAdmitted = true
			_ = store.UpdateTask(ctx, cur)
			s.running = map[string]bool{p.Task.RootID + "/" + c.Task.ID: true}
			if err := s.executeTask(ctx, p.Task.RootID, c.Task.ID); err != nil {
				t.Fatal(err)
			}

			got, _ := store.GetTask(ctx, c.Task.ID)
			t.Logf("auto_advance=%v -> stage=%d status=%s", tc.autoAdvance, got.CurrentStageIndex, got.Status)
			if got.CurrentStageIndex != tc.wantStage {
				t.Errorf("阶段索引 = %d, 期望 %d", got.CurrentStageIndex, tc.wantStage)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("状态 = %s, 期望 %s", got.Status, tc.wantStatus)
			}
		})
	}
}

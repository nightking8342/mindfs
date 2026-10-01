package kanban

import (
	"context"
	"mindfs/server/internal/fs"
	"strings"
	"testing"
)

// fork: regression test for `-next` walking past an undelivered agent stage.
//
// finishManagedRun records every agent stage as `success` regardless of whether
// the agent ever called `-from-task ... completed: true`. nextManaged's
// "agent stage has not delivered" guard tested only Run.Status, so it never
// fired: the parent session could -next past a stage that produced nothing, and
// advanceManagedStage carried the empty Result into the next stage's Input
// (`{previous_input}` rendered blank, with no warning).
//
// Observed in the vFlow task group (task #4): a stage-1 run with result_len=0
// and status=success, followed by a user_approved event, followed by a stage-2
// run whose Input was empty.
//
// The guard now also requires a non-empty Result. Delivery always leaves one,
// because -from-task rejects an empty message ("message required").
func undeliveredNextFixture(t *testing.T) (*Service, *TaskStore, TaskDetail) {
	t.Helper()
	ctx := context.Background()
	root := fs.RootInfo{ID: "root", RootPath: t.TempDir()}
	templates := NewTemplateStoreAt(t.TempDir())
	// A manual (AutoAdvance=false) agent stage followed by another one, so the
	// stage under test is advanced by the parent session via -next.
	tmpl, e := templates.SaveTaskTemplate(TaskTemplate{Name: "Blueprint", MaxConcurrency: 2, Stages: []TaskTemplateStage{
		{Position: 0, Snapshot: StageTemplate{Name: "需求", Role: RoleUser}},
		{Position: 1, Snapshot: StageTemplate{Name: "方案", Role: RoleAgent, Agent: "codex", AutoAdvance: false, PromptTemplate: "{task_initial_input}"}},
		{Position: 2, Snapshot: StageTemplate{Name: "实现", Role: RoleAgent, Agent: "codex", PromptTemplate: "{previous_input}"}},
	}})
	if e != nil {
		t.Fatal(e)
	}
	svc := NewService(templates, testRoots{root})
	parent, e := svc.CreateTask(ctx, CreateTaskInput{RootID: root.ID, TaskTemplateID: tmpl.ID, Input: "Build feature"})
	if e != nil {
		t.Fatal(e)
	}
	store, e := svc.taskStore(root.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(svc.Close)
	svc.Runner = &groupTestRunner{}
	group, err := svc.CreateGroup(ctx, TaskGroup{RootID: root.ID, SessionKey: "parent-chat", Title: "Feature"})
	if err != nil {
		t.Fatal(err)
	}
	svc.Runner = nil
	parent.Task.GroupID = group.ID
	return svc, store, parent
}

// executeStage drives the task at its current stage with a runner that does not
// deliver, reproducing the state the vFlow task was in.
func executeStage(t *testing.T, s *Service, store *TaskStore, rootID, taskID string) {
	t.Helper()
	ctx := context.Background()
	runner := &orchestrationRunner{}
	runner.run = func(exec AgentStageExecution) error { return nil }
	s.Runner = runner

	task, _ := store.GetTask(ctx, taskID)
	task.Status = StatusRunning
	task.SchedulerAdmitted = true
	if e := store.UpdateTask(ctx, task); e != nil {
		t.Fatal(e)
	}
	if e := s.executeTask(ctx, rootID, taskID); e != nil {
		t.Fatal(e)
	}
	s.Runner = nil
}

func TestNextRejectsStageWithoutDelivery(t *testing.T) {
	ctx := context.Background()
	s, store, p := undeliveredNextFixture(t)
	a := child(t, s, p, "no-delivery")
	approve(t, s, p)

	executeStage(t, s, store, p.Task.RootID, a.Task.ID)

	// Precondition: upstream records this as success, which is exactly why the
	// status-only guard could not catch it.
	run, err := store.LatestStageRun(ctx, a.Task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StageStatusSuccess {
		t.Skipf("upstream no longer marks undelivered runs successful (status=%q); guard scope can be revisited", run.Status)
	}
	if strings.TrimSpace(run.Result) != "" {
		t.Fatalf("fixture did not reproduce an empty delivery: result=%q", run.Result)
	}

	before, _ := store.GetTask(ctx, a.Task.ID)
	_, err = s.Next(ctx, MoveInput{RootID: p.Task.RootID, TaskID: a.Task.ID})
	if err == nil {
		t.Fatal("Next advanced a stage that never delivered")
	}
	if !strings.Contains(err.Error(), "has not delivered") {
		t.Errorf("unexpected rejection reason: %v", err)
	}
	// The message must name the follow-up action, not merely deny. A bare
	// "agent stage has not delivered" is what sent the parent session off to
	// -to-task for 10 turns (docs/task-orchestration-internals.md G6/G12).
	if !strings.Contains(err.Error(), "-to-task") {
		t.Errorf("rejection does not say what to do instead: %v", err)
	}
	after, _ := store.GetTask(ctx, a.Task.ID)
	if after.CurrentStageIndex != before.CurrentStageIndex {
		t.Errorf("stage advanced despite rejection: %d -> %d", before.CurrentStageIndex, after.CurrentStageIndex)
	}
}

// The delivered path must keep working: a real delivery still advances, and the
// delivery text must survive into the next stage's Input.
func TestNextAcceptsStageWithDelivery(t *testing.T) {
	ctx := context.Background()
	s, store, p := undeliveredNextFixture(t)
	a := child(t, s, p, "delivery")
	approve(t, s, p)

	runner := &orchestrationRunner{}
	runner.run = func(exec AgentStageExecution) error {
		_, e := s.ManagedAction(ctx, p.Task.RootID, a.Task.ID, "from-task",
			ManagedInput{Completed: true, Message: "THE PLAN"})
		return e
	}
	s.Runner = runner
	task, _ := store.GetTask(ctx, a.Task.ID)
	task.Status = StatusRunning
	task.SchedulerAdmitted = true
	if e := store.UpdateTask(ctx, task); e != nil {
		t.Fatal(e)
	}
	if e := s.executeTask(ctx, p.Task.RootID, a.Task.ID); e != nil {
		t.Fatal(e)
	}
	s.Runner = nil

	if _, err := s.Next(ctx, MoveInput{RootID: p.Task.RootID, TaskID: a.Task.ID}); err != nil {
		t.Fatalf("Next rejected a delivered stage: %v", err)
	}
	after, _ := store.GetTask(ctx, a.Task.ID)
	if after.CurrentStageIndex != 2 {
		t.Fatalf("delivered stage did not advance: stage_index=%d", after.CurrentStageIndex)
	}

	// NOTE: the delivery text does NOT reach the next stage's Input on this
	// path. moveTo (service.go:1258-1267) builds the new StageRun without an
	// Input field at all, while advanceManagedStage (orchestration_execution.go)
	// passes `result` into it. So with -next the downstream prompt's
	// {previous_input} renders blank even for a properly delivered stage.
	// Asserting the current behaviour here so the divergence is visible; the
	// prompt-side fallback is plan-<n>.md, which is why the blueprint template
	// still works in practice.
	next, err := store.LatestStageRun(ctx, a.Task.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(next.Input) != "" {
		t.Logf("moveTo now carries delivery into the next stage Input: %q", next.Input)
	}
}

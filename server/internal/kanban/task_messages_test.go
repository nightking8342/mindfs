package kanban

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrdinaryTaskMessagesPreserveLifecycleAndSerialize(t *testing.T) {
	for _, status := range []string{StatusWaitingUser, StatusPaused, StatusSuccess, StatusFail} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			s, store, base := orchestrationFixture(t)
			task, err := store.GetTask(ctx, base.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			task.CurrentStageIndex, task.MainSessionKey, task.Status = 1, "ordinary-chat", status
			// Ordinary tasks keep their scheduler slot after an agent stage
			// finishes and waits for user input.
			task.SchedulerAdmitted = status == StatusWaitingUser
			task.CompletedAt, task.BlockReason = "existing-completion", "existing-reason"
			if err := store.UpdateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
				if exec.Run.SessionKey != task.MainSessionKey || exec.Task.SchedulerAdmitted != task.SchedulerAdmitted || exec.Stage.Agent != "codex" {
					t.Error("message changed session, agent, or acquired a task slot")
				}
				switch calls.Add(1) {
				case 1:
					if exec.Prompt != "first user message" {
						t.Error("message was wrapped in orchestration instructions")
					}
					_, err := s.ManagedAction(ctx, task.RootID, task.ID, "to-task", ManagedInput{Message: "second user message"})
					return err
				case 2:
					if exec.Prompt != "second user message" {
						t.Error("follow-up message lost or duplicated")
					}
				default:
					t.Error("unexpected extra message turn")
				}
				return nil
			}}
			if _, err := s.ManagedAction(ctx, task.RootID, task.ID, "to-task", ManagedInput{Message: "first user message"}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				inbox, err := store.Inbox(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if calls.Load() == 2 && len(inbox) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("messages not delivered: calls=%d inbox=%+v", calls.Load(), inbox)
				}
				time.Sleep(10 * time.Millisecond)
			}
			current, err := store.GetTask(ctx, task.ID)
			if err != nil || current.Status != status || current.CurrentStageIndex != task.CurrentStageIndex || current.CompletedAt != task.CompletedAt || current.BlockReason != task.BlockReason || current.SchedulerAdmitted != task.SchedulerAdmitted {
				t.Fatalf("user messages changed task lifecycle: %+v %v", current, err)
			}
		})
	}
}

func TestOrdinaryTaskMessagesReachStageSession(t *testing.T) {
	for _, policy := range []string{SessionReuseSameStage, SessionReuseAlwaysNew} {
		for _, tc := range []struct {
			name, mainKey string
			manual        bool
		}{
			{name: "no_main_session"},
			{name: "prefer_stage_session", mainKey: "older-main-chat"},
			{name: "manual_stage", manual: true},
		} {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				s, store, base := orchestrationFixture(t)
				task, err := store.GetTask(ctx, base.Task.ID)
				if err != nil {
					t.Fatal(err)
				}
				tmpl, err := s.TaskExecutionTemplate(task)
				if err != nil {
					t.Fatal(err)
				}
				tmpl.Stages[1].Snapshot.SessionReusePolicy = policy
				tmpl.Stages[1].Snapshot.Model = "stage-model"
				if tc.manual {
					tmpl.Stages = append(tmpl.Stages, TaskTemplateStage{Position: 2, Snapshot: StageTemplate{Name: "Review", Role: RoleUser}})
				}
				if _, err := s.Templates.SaveTaskTemplate(tmpl); err != nil {
					t.Fatal(err)
				}
				task.MainSessionKey, task.CurrentStageIndex = tc.mainKey, 1
				if tc.manual {
					task.CurrentStageIndex = 2
				}
				task.Status, task.SchedulerAdmitted = StatusWaitingUser, true
				now := time.Now().UTC()
				for i, key := range []string{"old-stage-chat", "latest-stage-chat"} {
					created := now.Add(time.Duration(i) * time.Second)
					run := StageRun{ID: newID("run"), TaskID: task.ID, StageIndex: 1, StageName: "Coordinate", Role: RoleAgent, Status: StageStatusSuccess, SessionKey: key, CreatedAt: created, UpdatedAt: created}
					if err := store.MoveTask(ctx, task, run, TaskEvent{ID: newID("event"), TaskID: task.ID, Type: "test", Payload: "{}", CreatedAt: created}); err != nil {
						t.Fatal(err)
					}
				}
				var calls atomic.Int32
				s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
					calls.Add(1)
					if exec.Run.SessionKey != "latest-stage-chat" || exec.Prompt != "follow-up" || exec.Stage.SessionReusePolicy != policy || exec.Stage.Model != "stage-model" {
						t.Errorf("wrong message target or configuration: %+v", exec)
					}
					return nil
				}}
				if _, err := s.ManagedAction(ctx, task.RootID, task.ID, "to-task", ManagedInput{Message: "follow-up"}); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					inbox, err := store.Inbox(ctx, task.ID)
					if err != nil {
						t.Fatal(err)
					}
					if calls.Load() == 1 && len(inbox) == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("stage message not delivered: calls=%d inbox=%+v", calls.Load(), inbox)
					}
					time.Sleep(10 * time.Millisecond)
				}
				current, err := store.GetTask(ctx, task.ID)
				if err != nil || current.MainSessionKey != tc.mainKey || current.CurrentStageIndex != task.CurrentStageIndex || current.Status != task.Status || !current.SchedulerAdmitted {
					t.Fatalf("message changed task lifecycle or main session: %+v %v", current, err)
				}
			})
		}
	}
}

func TestOrdinaryTaskMessageWaitsForSessionAndRetainsFailedMessage(t *testing.T) {
	ctx := context.Background()
	s, store, base := orchestrationFixture(t)
	task, err := store.GetTask(ctx, base.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagedAction(ctx, task.RootID, task.ID, "to-task", ManagedInput{Message: "queued user message"}); err != nil {
		t.Fatal(err)
	}
	var calls int
	wantErr := errors.New("session unavailable")
	s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
		calls++
		if exec.Prompt != "queued user message" {
			t.Error("queued content changed")
		}
		return wantErr
	}}
	if err := s.executeTaskMessages(ctx, task.RootID, task.ID); err != nil || calls != 0 {
		t.Fatalf("message started task without a session: %v calls=%d", err, calls)
	}
	task.CurrentStageIndex, task.MainSessionKey = 1, "created-chat"
	if err := store.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.executeTaskMessages(ctx, task.RootID, task.ID); !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("message did not reach created session: %v calls=%d", err, calls)
	}
	inbox, err := store.Inbox(ctx, task.ID)
	if err != nil || len(inbox) != 1 || inbox[0].StageRunID == "" {
		t.Fatalf("failed message was lost or left eligible for an endless retry: %+v %v", inbox, err)
	}
}

func TestOrdinaryTaskMessageValidation(t *testing.T) {
	ctx := context.Background()
	s, store, base := orchestrationFixture(t)
	for _, tc := range []struct {
		action string
		input  ManagedInput
	}{
		{"to-task", ManagedInput{Message: " "}},
		{"to-task", ManagedInput{Message: "done", Completed: true}},
		{"from-task", ManagedInput{Message: "no parent"}},
	} {
		if _, err := s.ManagedAction(ctx, base.Task.RootID, base.Task.ID, tc.action, tc.input); err == nil {
			t.Fatalf("accepted invalid ordinary task message: %+v", tc)
		}
	}
	task, err := store.GetTask(ctx, base.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = StatusCancelled
	if err := store.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagedAction(ctx, task.RootID, task.ID, "to-task", ManagedInput{Message: "cancelled"}); err == nil {
		t.Fatal("cancelled task accepted message")
	}
	inbox, err := store.Inbox(ctx, task.ID)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("rejected messages persisted: %+v %v", inbox, err)
	}
}

func TestTaskMessagesIgnoreSchedulingAndSerializeReplies(t *testing.T) {
	ctx := context.Background()
	s, store, g := groupFixture(t)
	defer s.Close()
	target := groupTask(t, s, g, "target")
	busy := groupTask(t, s, g, "occupies shared workspace")
	busy.Task.Status, busy.Task.SchedulerAdmitted = StatusRunning, true
	if err := store.UpdateTask(ctx, busy.Task); err != nil {
		t.Fatal(err)
	}
	task := target.Task
	task.CurrentStageIndex, task.MainSessionKey, task.Status = 1, "existing-chat", StatusWaitingUser
	if err := store.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := StageRun{ID: newID("run"), TaskID: task.ID, StageIndex: 1, StageName: "Coordinate", Role: RoleAgent, Status: StageStatusSuccess, SessionKey: task.MainSessionKey, CreatedAt: now, UpdatedAt: now}
	if err := store.MoveTask(ctx, task, run, TaskEvent{ID: newID("event"), TaskID: task.ID, Type: "test", Payload: "{}", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Neither an unpublished/paused group nor occupied task slots block messages.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_groups SET status='paused',project_context='shared context must not repeat' WHERE id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	received := make(chan string, 2)
	s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
		if exec.Run.SessionKey != "existing-chat" || exec.Task.SchedulerAdmitted {
			t.Error("reply acquired task slot or changed session")
		}
		n := calls.Add(1)
		if n == 1 {
			if exec.Prompt != "first reply" {
				t.Error("first reply missing")
			}
			if _, err := s.ManagedAction(ctx, g.RootID, task.ID, "to-task", ManagedInput{Message: "second reply"}); err != nil {
				return err
			}
			current, _ := store.GetTask(ctx, task.ID)
			if current.Status != StatusRunning {
				t.Error("new message disrupted active turn")
			}
		} else if n == 2 {
			if exec.Prompt != "second reply" {
				t.Error("reply duplicated or lost")
			}
		} else {
			t.Error("unexpected extra turn")
		}
		_, err := s.ManagedAction(ctx, g.RootID, task.ID, "from-task", ManagedInput{Message: "done", Completed: true})
		received <- exec.Prompt
		return err
	}}
	if _, err := s.ManagedAction(ctx, g.RootID, task.ID, "to-task", ManagedInput{Message: "first reply"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("reply blocked by task scheduling")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := store.GetTask(ctx, task.ID)
		inbox, e := store.Inbox(ctx, task.ID)
		if err == nil && e == nil && current.Status == StatusSuccess && len(inbox) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery not finalized: %+v inbox=%+v err=%v", current, inbox, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := store.GetTask(ctx, busy.Task.ID)
	if err != nil || !current.SchedulerAdmitted {
		t.Fatal("other task slot changed")
	}
}

func TestPausedGroupStillReceivesReports(t *testing.T) {
	ctx := context.Background()
	s, store, g := groupFixture(t)
	defer s.Close()
	task := groupTask(t, s, g, "question")
	if _, err := store.db.ExecContext(ctx, `UPDATE task_groups SET status='paused' WHERE id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	s.Runner = &groupTestRunner{turn: func(_ TaskGroup, prompt string) error { received <- prompt; return nil }}
	if _, err := s.ManagedAction(ctx, g.RootID, task.Task.ID, "from-task", ManagedInput{Message: "question while paused"}); err != nil {
		t.Fatal(err)
	}
	select {
	case prompt := <-received:
		if !strings.Contains(prompt, "question while paused") {
			t.Fatal("report missing")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pause blocked report")
	}
	current, err := store.getGroup(ctx, g.ID)
	if err != nil || current.Status != "paused" {
		t.Fatalf("communication resumed task scheduling: %+v %v", current, err)
	}
}

func TestTaskMessageDoesNotAdvanceManualStage(t *testing.T) {
	ctx := context.Background()
	s, store, g := groupFixture(t)
	defer s.Close()
	target := groupTask(t, s, g, "manual input")
	tmpl, err := s.TaskExecutionTemplate(target.Task)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.Stages = append(tmpl.Stages, TaskTemplateStage{Position: 2, Snapshot: StageTemplate{Name: "Review", Role: RoleUser}})
	if _, err = s.Templates.SaveTaskTemplate(tmpl); err != nil {
		t.Fatal(err)
	}
	task := target.Task
	task.CurrentStageIndex, task.MainSessionKey, task.Status = 2, "existing-chat", StatusWaitingUser
	if err = store.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ManagedAction(ctx, g.RootID, task.ID, "to-task", ManagedInput{Message: "clarify requirements"}); err != nil {
		t.Fatal(err)
	}
	s.Runner = &orchestrationRunner{run: func(exec AgentStageExecution) error {
		if exec.Run.SessionKey != "existing-chat" || exec.Prompt != "clarify requirements" {
			t.Error("manual-stage conversation did not receive message")
		}
		if _, err := s.ManagedAction(ctx, g.RootID, task.ID, "from-task", ManagedInput{Message: "answer"}); err != nil {
			return err
		}
		return nil
	}}
	if err = s.executeTaskMessages(ctx, g.RootID, task.ID); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current.CurrentStageIndex != 2 || current.Status != StatusWaitingUser || current.SchedulerAdmitted {
		t.Fatalf("message advanced manual stage: %+v %v", current, err)
	}
}

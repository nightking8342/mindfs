package kanban

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ManagedInput struct {
	Message   string `json:"message"`
	Completed bool   `json:"completed,omitempty"`

	PlanVersion *int `json:"plan_version"`

	// fork: optional target stage for a -to-task message, addressed by stage
	// index. Without it a message goes to the task's current stage. With it the
	// task is moved back to that stage and the message is delivered to that
	// stage's own session, so a rework request reaches whoever did the work
	// instead of whoever happened to run last.
	StageIndex *int `json:"stage_index,omitempty"`
}

// taskReport binds a stored report to its execution independently of the
// receiver's inbox processing. This is internal metadata, not a CLI parameter.
type taskReport struct {
	ManagedInput
	ExecutionID string `json:"execution_id"`
}

func (s *Service) ManagedAction(ctx context.Context, root, id, action string, in ManagedInput) (TaskDetail, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	store, t, tmpl, err := s.loadForMove(ctx, root, id)
	if err != nil {
		return TaskDetail{}, err
	}
	if t.GroupID == "" && action != "to-task" {
		return TaskDetail{}, errors.New("not an orchestrated task")
	}
	if t.GroupID != "" && action != "cancel" {
		g, e := store.getGroup(ctx, t.GroupID)
		if e != nil {
			return TaskDetail{}, e
		}
		if g.Status == "success" || g.Status == "cancelled" {
			return TaskDetail{}, errors.New("task group is terminal")
		}
	}
	if in.Completed && action != "from-task" {
		return TaskDetail{}, errors.New("completed is only supported by from-task")
	}
	now := time.Now().UTC()
	event := TaskEvent{ID: newID("event"), TaskID: id, Type: action, Payload: eventPayload(in), CreatedAt: now}
	switch action {
	case "to-task", "from-task":
		if strings.TrimSpace(in.Message) == "" {
			return TaskDetail{}, errors.New("message required")
		}
		owner := t.GroupID
		if action == "to-task" {
			if t.Status == StatusCancelled {
				return TaskDetail{}, errors.New("recipient task is cancelled")
			}
			if owner == "" {
				// fork: stage_index only means something for a grouped task, whose
				// stages each get their own session. An ordinary task keeps one
				// conversation, so accepting the field here would silently do
				// nothing while the caller believed the stage had been chosen.
				if in.StageIndex != nil {
					return TaskDetail{}, errors.New("stage_index is only supported for tasks in a task group")
				}
				// Ordinary tasks receive user messages without changing their
				// stage, completion state, or scheduler admission.
				event.ReceiverTaskID = t.ID
				if err = store.AddEvent(ctx, event); err != nil {
					return TaskDetail{}, err
				}
				s.Schedule(root)
				return s.changed(ctx, store, id)
			}
			// fork: a task that already succeeded is NOT reopenable. Upstream reset
			// it in place (stage pointer back to the nearest agent stage) but never
			// touched its downstream tasks, so a reopened task could leave
			// dependents holding results built on a superseded upstream. Policy
			// here: a finished task stays finished; rework is a new task, which
			// also removes the cascade question entirely.
			if t.Status == StatusSuccess {
				return TaskDetail{}, errors.New("task has already been completed; append a new task to the group instead of sending a message to a finished one")
			}
			// fork: an explicit stage rewinds the task to it before delivery, so
			// the message reaches the session that did the work rather than
			// whichever stage ran last. Validated before the run-state check so a
			// bad index always gets an accurate message.
			target := t.CurrentStageIndex
			if in.StageIndex != nil {
				want := *in.StageIndex
				if want < 0 || want >= len(tmpl.Stages) {
					return TaskDetail{}, fmt.Errorf("stage_index %d out of range (template has %d stages)", want, len(tmpl.Stages))
				}
				if want > t.CurrentStageIndex {
					return TaskDetail{}, fmt.Errorf("stage_index %d is ahead of the current stage %d; use -next to advance", want, t.CurrentStageIndex)
				}
				if tmpl.Stages[want].Snapshot.Role != RoleAgent {
					return TaskDetail{}, fmt.Errorf("stage_index %d is a %s stage; only agent stages can be addressed", want, tmpl.Stages[want].Snapshot.Role)
				}
				if t.SchedulerAdmitted || t.Status == StatusRunning {
					return TaskDetail{}, errors.New("task is running; wait for the current stage to finish before addressing an earlier stage")
				}
				target = want
			}
			if !t.SchedulerAdmitted && t.Status != StatusRunning {
				// A rewind is a correction on top of existing work: the stage
				// pointer moves, but nothing about the work is discarded. The prior
				// stage runs stay as history and the target stage's own run keeps
				// its session, which is what the reply is delivered to.
				t.CurrentStageIndex = target
				if t.CurrentStageIndex == 0 || tmpl.Stages[t.CurrentStageIndex].Snapshot.Role == RoleAgent {
					t.Status = StatusPending
					if t.MainSessionKey != "" {
						t.Status = StatusWaitingUser
					}
				}
				t.BlockReason = ""
				t.CompletedAt = ""
				t.AuxFlags = TaskAuxFlags{}
			}
			event.ReceiverTaskID = t.ID
			if owner != "" {
				event.TaskID = owner
			}
		} else {
			event.ReceiverTaskID = owner
			run, e := store.LatestStageRun(ctx, t.ID, t.CurrentStageIndex)
			if in.Completed {
				if e != nil {
					return TaskDetail{}, e
				}
				if t.Status != StatusRunning || run.Status != StageStatusRunning {
					return TaskDetail{}, errors.New("completion requires an active task execution")
				}
				// Delivery is recorded as a message. It only takes effect after
				// successful execution exit; normal deliveries are collected for
				// the final group review rather than waking the parent one by one.
				event.ReceiverTaskID = ""
			}
			if e == nil {
				event.Payload = eventPayload(taskReport{ManagedInput: in, ExecutionID: run.ID})
			}
		}
	case "cancel":
		if strings.TrimSpace(in.Message) == "" {
			return TaskDetail{}, errors.New("reason required")
		}
		run, e := store.LatestStageRun(ctx, id, t.CurrentStageIndex)
		if e != nil {
			return TaskDetail{}, e
		}
		event.StageRunID = run.ID
		event.Type = "intent_cancel"
		executing := t.SchedulerAdmitted || t.Status == StatusRunning
		t.Status = StatusCancelled
		if executing { // Release only after execution exits.
			if err = store.AddEvent(ctx, event); err != nil {
				return TaskDetail{}, err
			}
			t.UpdatedAt = now
			if err = store.UpdateTask(ctx, t); err != nil {
				return TaskDetail{}, err
			}
			if stopper, ok := s.Runner.(interface {
				StopTaskExecution(context.Context, Task) error
			}); ok {
				go func() { _ = stopper.StopTaskExecution(context.Background(), t) }()
			}
			return s.changed(ctx, store, id)
		}
	default:
		return TaskDetail{}, errors.New("unsupported task operation; use to-task, from-task or cancel")
	}
	t.UpdatedAt = now
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskDetail{}, err
	}
	defer tx.Rollback()
	if err = updateTaskCore(ctx, tx, t); err != nil {
		return TaskDetail{}, err
	}
	if action == "to-task" {
		if err = bumpGroup(ctx, tx, t.GroupID); err != nil {
			return TaskDetail{}, err
		}
	}
	if err = insertTaskEvent(ctx, tx, event); err != nil {
		return TaskDetail{}, err
	}
	if err = tx.Commit(); err != nil {
		return TaskDetail{}, err
	}
	if action == "to-task" {
		s.taskGroupChanged(ctx, store, t.GroupID)
	}
	s.Schedule(root)
	return s.changed(ctx, store, id)
}

func (s *Service) managedReady(ctx context.Context, store *TaskStore, t Task) bool {
	if t.BlockReason != "" && t.BlockReason != "publish_approval" {
		return false
	}
	if t.GroupID != "" {
		if !t.Published {
			return false
		}
		g, e := store.getGroup(ctx, t.GroupID)
		if e != nil || !g.Published || (g.Status != "active" && g.Status != "coordinating") {
			return false
		}
		deps, e := store.Dependencies(ctx, t.ID)
		if e != nil {
			return false
		}
		for _, id := range deps {
			d, e := store.GetTask(ctx, id)
			if e != nil || d.GroupID != t.GroupID || d.Status != StatusSuccess || d.SchedulerAdmitted || d.BlockReason != "" {
				return false
			}
		}
		return true
	}
	return false
}

func (s *Service) refreshManaged(ctx context.Context, store *TaskStore, all []Task) error {
	for _, t := range all {
		if t.GroupID != "" && t.Status == StatusPending && s.managedReady(ctx, store, t) {
			if err := store.UpdateTaskStatus(ctx, t.ID, StatusQueued, nil, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) executeManaged(ctx context.Context, store *TaskStore, t Task, tmpl TaskTemplate) error {
	return s.executeManagedTurn(ctx, store, t, tmpl, false)
}

func (s *Service) executeManagedTurn(ctx context.Context, store *TaskStore, t Task, tmpl TaskTemplate, messageTurn bool) error {
	s.opMu.Lock()
	current, e := store.GetTask(ctx, t.ID)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	t = current
	tmpl, e = s.TaskExecutionTemplate(t)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	if !messageTurn && (!t.SchedulerAdmitted || t.Status != StatusRunning) {
		s.opMu.Unlock()
		return nil
	}
	if messageTurn && t.Status == StatusCancelled {
		s.opMu.Unlock()
		return nil
	}
	if messageTurn {
		t.Status = StatusRunning
	}
	now := time.Now().UTC()
	run, e := store.LatestStageRun(ctx, t.ID, t.CurrentStageIndex)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	if t.CurrentStageIndex < 0 || t.CurrentStageIndex >= len(tmpl.Stages) {
		s.opMu.Unlock()
		return errors.New("current stage out of range")
	}
	// Publication approves the initial input only; later user stages remain explicit gates.
	if t.CurrentStageIndex == 0 && len(tmpl.Stages) > 1 {
		detail, err := s.moveTo(ctx, store, t, tmpl, 1, "input_approved", StageStatusApproved, "published input")
		if err != nil {
			s.opMu.Unlock()
			return err
		}
		t = detail.Task
		run, e = store.LatestStageRun(ctx, t.ID, t.CurrentStageIndex)
		if e != nil {
			s.opMu.Unlock()
			return e
		}
	}
	if tmpl.Stages[t.CurrentStageIndex].Snapshot.Role == RoleUser {
		t.Status = StatusWaitingUser
		t.SchedulerAdmitted = false
		e = store.UpdateTask(ctx, t)
		if e == nil {
			s.changed(ctx, store, t.ID)
		}
		s.opMu.Unlock()
		return e
	}
	index := t.CurrentStageIndex
	if run.Status != StageStatusPending {
		run = StageRun{ID: newID("run"), TaskID: t.ID, StageIndex: index, StageName: tmpl.Stages[index].Snapshot.Name, Role: RoleAgent, Status: StageStatusPending, Trigger: "events", CreatedAt: now, UpdatedAt: now}
		if e = store.MoveTask(ctx, t, run, TaskEvent{ID: newID("event"), TaskID: t.ID, Type: "execution_queued", CreatedAt: now, Payload: "{}"}); e != nil {
			s.opMu.Unlock()
			return e
		}
	}
	stage := tmpl.Stages[index].Snapshot
	prompt := BuildAgentPrompt(stage.PromptTemplate, s.promptValues(ctx, store, t, tmpl, stage, run))
	basePrompt := prompt
	workflow := "Read mindfs -orchestration for CLI usage. Report with -from-task; set completed: true when the current stage is complete."
	prompt += "\n\n" + workflow
	if run.Input != "" && !strings.Contains(basePrompt, run.Input) {
		prompt += "\n\n## 本轮要求\n" + run.Input
	}
	g, e := store.getGroup(ctx, t.GroupID)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	if strings.TrimSpace(g.ProjectContext) != "" {
		prompt += "\n\n## 共享上下文\n" + g.ProjectContext
	}
	deps, e := store.Dependencies(ctx, t.ID)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	for _, id := range deps {
		d, e := store.GetDetail(ctx, id)
		if e != nil {
			s.opMu.Unlock()
			return e
		}
		prompt += "\n\n## 前置任务 " + id + " (" + d.Task.Status + ")\n"
		for i := len(d.StageRuns) - 1; i >= 0; i-- {
			if d.StageRuns[i].Result != "" {
				prompt += d.StageRuns[i].Result
				break
			}
		}
	}
	inbox, e := store.Inbox(ctx, t.ID)
	if e != nil {
		s.opMu.Unlock()
		return e
	}
	for _, m := range inbox {
		prompt += "\n\n## 父会话消息\n" + taskEventText(m)
	}
	exec := AgentStageExecution{RootID: t.RootID, RuntimeRootPath: t.WorktreePath, Task: t, Stage: stage, Run: run, Prompt: prompt}
	// Session creation is serialized with task edits; agent execution is not.
	key := t.MainSessionKey
	if !messageTurn {
		key, e = s.Runner.EnsureAgentSession(ctx, exec)
	} else if target, _, terr := taskMessageTarget(ctx, store, t, tmpl); terr == nil && strings.TrimSpace(target) != "" {
		// fork: a message turn goes to the session of the stage the task now sits
		// on, not to MainSessionKey. Every managed turn overwrites
		// MainSessionKey with the session of whichever stage ran last, so a
		// rework request for an earlier stage was delivered to a later stage's
		// session -- the acceptance reviewer -- regardless of stage_index.
		// Resolving it here (rather than trusting the caller) keeps this the one
		// place that decides delivery, so no path can bypass it.
		key = strings.TrimSpace(target)
	}
	if e != nil {
		s.opMu.Unlock()
		return s.managedRunnerError(ctx, store, t, run, e)
	}
	if len(inbox) > 0 && (messageTurn || (key == t.MainSessionKey && run.Trigger == "events")) {
		prompt = taskMessagesPrompt(inbox)
	}
	exec.Prompt = prompt
	t.MainSessionKey = key
	run.SessionKey = key
	run.Status = StageStatusRunning
	run.RenderedPrompt = prompt
	run.StartedAt = now.Format(time.RFC3339Nano)
	if e = store.UpdateTaskAndStageRun(ctx, t, run, TaskEvent{ID: newID("event"), TaskID: t.ID, StageRunID: run.ID, Type: "stage_started", Payload: "{}", CreatedAt: now}); e != nil {
		s.opMu.Unlock()
		return e
	}
	for _, m := range inbox {
		if _, e = store.db.ExecContext(ctx, `UPDATE task_events SET stage_run_id=? WHERE id=? AND handled_at=''`, run.ID, m.ID); e != nil {
			s.opMu.Unlock()
			return e
		}
	}
	exec.Task = t
	exec.Run = run
	s.changed(ctx, store, t.ID)
	s.opMu.Unlock()
	err := s.Runner.RunAgentStage(ctx, exec)
	return s.finishManagedRun(ctx, store, t, run, inbox, err)
}

// Finalize a group task execution after the agent turn exits.
func (s *Service) finishManagedRun(ctx context.Context, store *TaskStore, t Task, run StageRun, inbox []TaskEvent, err error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var e error
	t, e = store.GetTask(ctx, t.ID)
	if e != nil {
		return e
	}
	events, e := store.ListEvents(ctx, t.ID)
	if e != nil {
		return e
	}
	tmpl, e := s.TaskExecutionTemplate(t)
	if e != nil {
		return e
	}
	action := "waiting"
	message := "执行已结束，但未提交结果；请检查会话后继续"
	var intent ManagedInput
	if t.CurrentStageIndex < len(tmpl.Stages)-1 {
		action = "stage_done"
		message = ""
	}
	for _, ev := range events {
		if ev.StageRunID == run.ID && ev.Type == "intent_cancel" {
			action = "cancel"
			_ = json.Unmarshal([]byte(ev.Payload), &intent)
			message = intent.Message
		}
		if ev.Type == "from-task" && action != "cancel" {
			var report taskReport
			if json.Unmarshal([]byte(ev.Payload), &report) == nil && report.ExecutionID == run.ID && report.Completed {
				action = "complete"
				intent = report.ManagedInput
				message = report.Message
			}
		}
	}
	if err != nil && action != "cancel" {
		action = "fail"
		message = err.Error()
	}
	if action == "complete" {
		run.Result = message
		if t.CurrentStageIndex < len(tmpl.Stages)-1 {
			action = "stage_done"
		}
	}
	// Messages arriving during execution belong to the next turn. Do not unlock
	// downstream tasks while an unprocessed follow-up can still change the result.
	pendingMessages, e := store.Inbox(ctx, t.ID)
	if e != nil {
		return e
	}
	followup := false
	for _, m := range pendingMessages {
		if m.StageRunID != run.ID {
			followup = true
		}
	}
	if action != "cancel" && followup {
		action = "followup"
	}
	t.SchedulerAdmitted = false
	t.UpdatedAt = time.Now().UTC()
	run.FinishedAt = t.UpdatedAt.Format(time.RFC3339Nano)
	run.Status = StageStatusSuccess
	if err != nil {
		run.Status = StageStatusFail
	}
	switch action {
	case "stage_done":
		t.Status = StatusWaitingUser
	case "followup":
		t.Status = StatusPending
		if t.MainSessionKey != "" {
			t.Status = StatusWaitingUser
		}
		t.BlockReason = ""
		t.CompletedAt = ""
		run.Result = message
	case "complete":
		t.Status = StatusSuccess
		t.CompletedAt = run.FinishedAt
		t.BlockReason = ""
		run.Result = message
	case "cancel":
		t.Status = StatusCancelled
	case "fail":
		t.Status = StatusFail
		t.BlockReason = message
		run.Status = StageStatusFail
	case "waiting":
		t.Status = StatusWaitingUser
		t.BlockReason = ""
	default:
		t.Status = StatusPending
		if t.BlockReason == "publish_approval" {
			t.Status = StatusWaitingUser
		}
	}
	tx, e := store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if action == "stage_done" && tmpl.Stages[t.CurrentStageIndex].Snapshot.AutoAdvance {
		if e = advanceManagedStage(ctx, tx, &t, tmpl, message); e != nil {
			return e
		}
	}
	if e = updateTaskCore(ctx, tx, t); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE stage_runs SET status=?,result=?,finished_at=? WHERE id=?`, run.Status, run.Result, run.FinishedAt, run.ID); e != nil {
		return e
	}
	if err == nil {
		for _, m := range inbox {
			if _, e = tx.ExecContext(ctx, `UPDATE task_events SET handled_at=? WHERE id=?`, run.FinishedAt, m.ID); e != nil {
				return e
			}
		}
	}
	ev := TaskEvent{ID: newID("event"), TaskID: t.ID, StageRunID: run.ID, Type: "execution_" + action, Payload: eventPayload(map[string]string{"message": message}), CreatedAt: t.UpdatedAt}
	reported := false
	for _, ev := range events {
		if ev.Type == "from-task" {
			var report taskReport
			_ = json.Unmarshal([]byte(ev.Payload), &report)
			if report.ExecutionID == run.ID && !report.Completed {
				reported = true
			}
		}
	}
	if (action == "fail" || (action == "waiting" && !reported) || (action == "stage_done" && t.Status == StatusWaitingUser)) && t.GroupID != "" {
		ev.ReceiverTaskID = t.GroupID
		ev.StageRunID = ""
		if action == "stage_done" {
			ev.Type = "stage_confirmation_required"
			instruction := "当前阶段已完成，等待用户明确要求或手动推进；不要自行推进阶段。"
			if tmpl.Stages[t.CurrentStageIndex].Snapshot.Role == RoleUser {
				instruction = "任务已进入人工输入阶段，等待用户提交输入并确认。"
			}
			ev.Payload = eventPayload(map[string]string{"message": instruction, "result": message})
		}
	}
	if e = insertTaskEvent(ctx, tx, ev); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.changed(ctx, store, t.ID)
	return nil
}
func (s *Service) managedRunnerError(ctx context.Context, store *TaskStore, t Task, run StageRun, err error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if e := s.recordManagedFailure(ctx, store, t, err.Error()); e != nil {
		return e
	}
	return err
}

// Called with opMu held, including failures before an agent session is started.
func (s *Service) recordManagedFailure(ctx context.Context, store *TaskStore, t Task, message string) error {
	t.Status = StatusFail
	t.SchedulerAdmitted = false
	t.BlockReason = message
	t.UpdatedAt = time.Now().UTC()
	tx, e := store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = updateTaskCore(ctx, tx, t); e != nil {
		return e
	}
	if e = insertTaskEvent(ctx, tx, TaskEvent{ID: newID("event"), TaskID: t.ID, ReceiverTaskID: t.GroupID, Type: "execution_fail", Payload: eventPayload(map[string]string{"message": message}), CreatedAt: t.UpdatedAt}); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.changed(ctx, store, t.ID)
	return nil
}

// Complete means the current stage has delivered; only the last stage completes the task.
func advanceManagedStage(ctx context.Context, tx *sql.Tx, t *Task, tmpl TaskTemplate, result string) error {
	index := t.CurrentStageIndex + 1
	if index >= len(tmpl.Stages) {
		return nil
	}
	stage := tmpl.Stages[index].Snapshot
	t.CurrentStageIndex = index
	t.CompletedAt = ""
	t.Status = StatusQueued
	status := StageStatusPending
	if stage.Role == RoleUser {
		t.Status = StatusWaitingUser
		status = StageStatusWaitingUser
	}
	run := StageRun{ID: newID("run"), TaskID: t.ID, StageIndex: index, StageName: stage.Name, Role: stage.Role, Status: status, Input: result, CreatedAt: t.UpdatedAt, UpdatedAt: t.UpdatedAt}
	return insertStageRun(ctx, tx, run)
}

// A managed task still honors user and intermediate stages in its chosen template.
func (s *Service) nextManaged(ctx context.Context, in MoveInput) (TaskDetail, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	store, t, tmpl, err := s.loadForMove(ctx, in.RootID, in.TaskID)
	if err != nil {
		return TaskDetail{}, err
	}
	if t.SchedulerAdmitted || isTerminalStatus(t.Status) {
		return TaskDetail{}, errors.New("task is not ready to advance")
	}
	if t.GroupID != "" && !t.Published {
		return TaskDetail{}, errors.New("task is not published")
	}
	if t.CurrentStageIndex >= len(tmpl.Stages)-1 {
		return TaskDetail{}, errors.New("final agent stage requires a delivery message with completed: true")
	}
	run, err := store.LatestStageRun(ctx, t.ID, t.CurrentStageIndex)
	if err != nil {
		return TaskDetail{}, err
	}
	// fork: an empty Result means the agent never delivered. finishManagedRun
	// records the run as `success` either way, so the status check alone let
	// -next advance past an undelivered stage and carry that empty Result into
	// the next stage's Input (`{previous_input}` rendered blank, silently).
	// Observed in the vFlow task group: a run with result_len=0, followed by
	// user_approved, followed by a stage-2 run with an empty Input.
	// Delivery always leaves a non-empty Result, because -from-task rejects an
	// empty message ("message required"), so this is a faithful proxy.
	// fork: the message below used to be a bare "agent stage has not delivered",
	// which only denied the action without saying what to do instead. In the
	// vFlow task group the parent session read it, concluded "wrong command",
	// and spent 10 turns on -to-task -- which re-runs the stage without ever
	// advancing it. Error text that only negates is a documented failure mode
	// here (see docs/task-orchestration-internals.md G6/G12).
	if run.Role == RoleAgent && (run.Status != StageStatusSuccess || strings.TrimSpace(run.Result) == "") {
		if run.Status == StageStatusRunning {
			return TaskDetail{}, errors.New("agent stage has not delivered yet - the agent is still working. Wait for it to report with -from-task (completed: true), then run -next again. Do not substitute -to-task: it re-runs the current stage and never advances it.")
		}
		return TaskDetail{}, errors.New("agent stage has not delivered - the agent finished its turn without reporting. Send it a message with -to-task to ask for the delivery, then run -next. Do not use -to-task to advance: only -next advances a stage.")
	}
	d, err := s.moveTo(ctx, store, t, tmpl, t.CurrentStageIndex+1, "user_approved", StageStatusApproved, in.Reason)
	if err == nil {
		s.Schedule(in.RootID)
		s.changed(ctx, store, t.ID)
	}
	return d, err
}

// taskEventText keeps message content and results while hiding inbox bookkeeping.
func taskEventText(event TaskEvent) string {
	var content struct {
		Message string `json:"message"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal([]byte(event.Payload), &content); err != nil {
		return event.Payload
	}
	text := content.Message
	if content.Result != "" {
		text += "\nResult: " + content.Result
	}
	return text
}

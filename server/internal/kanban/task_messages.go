package kanban

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Messages to an existing conversation do not acquire a task scheduler slot.
// RunAgentStage uses the same session send lock as ordinary user messages.
// The task worker guard serializes messages with the task's current execution.
func (s *Service) deliverTaskMessages(ctx context.Context, store *TaskStore, tasks []Task) error {
	if s.Runner == nil {
		return nil
	}
	for _, task := range tasks {
		if task.Status == StatusCancelled {
			continue
		}
		s.mu.Lock()
		running := s.running[task.RootID+"/"+task.ID]
		s.mu.Unlock()
		// Ordinary tasks retain admission while waiting for user input. It
		// reserves a workflow slot, not an active agent turn. Managed tasks
		// release admission after execution, so their existing guard remains.
		if running || (task.SchedulerAdmitted && (task.GroupID != "" || task.Status == StatusRunning)) {
			continue
		}
		inbox, err := store.Inbox(ctx, task.ID)
		if err != nil {
			return err
		}
		fresh := false
		for _, event := range inbox {
			if event.StageRunID == "" {
				fresh = true
				break
			}
		}
		if !fresh {
			continue
		}
		tmpl, err := s.TaskExecutionTemplate(task)
		if err != nil {
			return err
		}
		key, _, err := taskMessageTarget(ctx, store, task, tmpl)
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		// Also recover replies queued by earlier versions without task admission.
		if task.GroupID != "" && (task.Status == StatusPending || task.Status == StatusQueued) {
			if err := store.UpdateTaskStatus(ctx, task.ID, StatusWaitingUser, nil, false); err != nil {
				return err
			}
		}
		s.runTask(task.RootID, task.ID, true)
	}
	return nil
}

func (s *Service) executeTaskMessages(ctx context.Context, root, id string) error {
	store, task, tmpl, err := s.loadForMove(ctx, root, id)
	if err != nil {
		return err
	}
	if task.Status == StatusCancelled {
		return nil
	}
	key, stage, err := taskMessageTarget(ctx, store, task, tmpl)
	if err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	if task.GroupID != "" && tmpl.Stages[task.CurrentStageIndex].Snapshot.Role == RoleAgent {
		return s.executeManagedTurn(ctx, store, task, tmpl, true)
	}
	// Ordinary task messages and managed manual-stage messages only continue
	// the conversation; they do not approve, reopen, or advance a task stage.
	s.opMu.Lock()
	inbox, err := store.Inbox(ctx, id)
	if err != nil {
		s.opMu.Unlock()
		return err
	}
	turn := newID("message")
	prompt := taskMessagesPrompt(inbox)
	for _, event := range inbox {
		if _, err = store.db.ExecContext(ctx, `UPDATE task_events SET stage_run_id=? WHERE id=? AND handled_at=''`, turn, event.ID); err != nil {
			s.opMu.Unlock()
			return err
		}
	}
	s.opMu.Unlock()
	if len(inbox) == 0 {
		return nil
	}
	err = s.Runner.RunAgentStage(ctx, AgentStageExecution{RootID: root, RuntimeRootPath: task.WorktreePath, Task: task, Stage: stage, Run: StageRun{SessionKey: key}, Prompt: strings.TrimSpace(prompt)})
	if err != nil {
		return err
	}
	for _, event := range inbox {
		if _, err = store.db.ExecContext(ctx, `UPDATE task_events SET handled_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), event.ID); err != nil {
			return err
		}
	}
	return nil
}

// Ordinary stages using same_stage or always_new keep their conversation on
// the stage run rather than on the task. A message continues that conversation;
// it must not call EnsureAgentSession and create a new always_new session.
// At a manual stage, use the nearest preceding agent stage's conversation.
func taskMessageTarget(ctx context.Context, store *TaskStore, task Task, tmpl TaskTemplate) (string, StageTemplate, error) {
	if task.CurrentStageIndex < 0 || task.CurrentStageIndex >= len(tmpl.Stages) {
		return "", StageTemplate{}, fmt.Errorf("current stage out of range")
	}
	stage := tmpl.Stages[task.CurrentStageIndex].Snapshot
	for i := task.CurrentStageIndex; i >= 0; i-- {
		if tmpl.Stages[i].Snapshot.Role != RoleAgent {
			continue
		}
		stage = tmpl.Stages[i].Snapshot
		if task.GroupID == "" {
			run, err := store.LatestStageRun(ctx, task.ID, i)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", stage, err
			}
			if err == nil && strings.TrimSpace(run.SessionKey) != "" {
				return strings.TrimSpace(run.SessionKey), stage, nil
			}
		}
		break
	}
	return strings.TrimSpace(task.MainSessionKey), stage, nil
}

// Existing conversations already contain execution identity and project context.
func taskMessagesPrompt(inbox []TaskEvent) string {
	messages := make([]string, 0, len(inbox))
	for _, event := range inbox {
		messages = append(messages, taskEventText(event))
	}
	return strings.Join(messages, "\n\n")
}

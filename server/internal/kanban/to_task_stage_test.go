package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"mindfs/server/internal/fs"
	"strings"
	"testing"
)

// fork: `-to-task` can address a specific stage.
//
// Upstream had no way to express "send this to the implementer", so a rework
// request for a task sitting at its acceptance stage was delivered to whatever
// session MainSessionKey happened to hold -- and every managed turn overwrites
// MainSessionKey with the session of the stage that ran last, so that was the
// reviewer's. The reviewer then edited code, destroying the independence that
// makes the acceptance stage worth having.
//
// ManagedInput.StageIndex now rewinds the task to the addressed stage before
// delivery, and taskMessageTarget resolves that stage's own session for grouped
// tasks (upstream only did so when GroupID == "").

func stageTargetFixture(t *testing.T) (*Service, *TaskStore, TaskDetail) {
	t.Helper()
	ctx := context.Background()
	root := fs.RootInfo{ID: "root", RootPath: t.TempDir()}
	templates := NewTemplateStoreAt(t.TempDir())
	// user(0) -> agent(1) -> agent(2) -> agent(3). Stage 1 must be addressable
	// while the task is parked on a *middle* agent stage: if stage 2 were last,
	// the task would already be `success` and closed to messages, and the stage
	// under test would not have been overwritten in MainSessionKey yet.
	tmpl, e := templates.SaveTaskTemplate(TaskTemplate{Name: "Blueprint", MaxConcurrency: 2, Stages: []TaskTemplateStage{
		{Position: 0, Snapshot: StageTemplate{Name: "需求", Role: RoleUser}},
		{Position: 1, Snapshot: StageTemplate{Name: "方案", Role: RoleAgent, Agent: "codex", AutoAdvance: true, PromptTemplate: "{task_initial_input}"}},
		{Position: 2, Snapshot: StageTemplate{Name: "实现", Role: RoleAgent, Agent: "codex", AutoAdvance: false, PromptTemplate: "{previous_input}"}},
		{Position: 3, Snapshot: StageTemplate{Name: "验收", Role: RoleAgent, Agent: "codex", PromptTemplate: "{previous_input}"}},
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

// advanceToStage2 runs the task's stages 1 and 2 so it parks on stage 2 (an
// agent stage that AutoAdvance leaves waiting for the parent session). By then
// stage 1's session is recorded on its own run, and MainSessionKey has moved on
// to stage 2's session -- which is exactly the confusion under test.
func advanceToStage2(t *testing.T, s *Service, store *TaskStore, p TaskDetail, id string, runner Runner) {
	t.Helper()
	ctx := context.Background()
	// stageSessionRunner gives each stage its own session key and delivers from
	// each one. orchestrationRunner reuses MainSessionKey across stages, which
	// would collapse them onto one key and hide the confusion under test.
	//
	// The loop keeps going until the task is *parked* on stage 2 with its turn
	// finished: only then does stage 2 record a session of its own and displace
	// MainSessionKey from stage 1's, which is the situation under test.
	s.Runner = runner
	for i := 0; i < 6; i++ {
		got, _ := store.GetTask(ctx, id)
		// Parked means stage 2's own turn ran to completion, leaving it with a
		// recorded session while the task waits for the parent session.
		if got.CurrentStageIndex == 2 && !got.SchedulerAdmitted && got.Status == StatusWaitingUser {
			break
		}
		if got.Status == StatusPending || got.Status == StatusQueued {
			got.Status = StatusRunning
			got.SchedulerAdmitted = true
			_ = store.UpdateTask(ctx, got)
		}
		if e := s.executeTask(ctx, p.Task.RootID, id); e != nil {
			t.Fatal(e)
		}
	}
	s.Runner = nil

	got, _ := store.GetTask(ctx, id)
	if got.CurrentStageIndex != 2 {
		t.Fatalf("fixture did not park on stage 2: stage=%d status=%s", got.CurrentStageIndex, got.Status)
	}
	if got.Status == StatusSuccess {
		t.Fatalf("fixture overshot into a completed task: %s", got.Status)
	}
}

// stageSessionRunner records a distinct session key per stage and delivers from
// each one, mirroring a template whose agent stages each get their own session.
type stageSessionRunner struct {
	groupTestRunner
	s      *Service
	rootID string
	taskID string
}

func (r *stageSessionRunner) EnsureAgentSession(_ context.Context, exec AgentStageExecution) (string, error) {
	return fmt.Sprintf("session-stage-%d", exec.Run.StageIndex), nil
}

func (r *stageSessionRunner) RunAgentStage(ctx context.Context, exec AgentStageExecution) error {
	_, err := r.s.ManagedAction(ctx, r.rootID, r.taskID, "from-task",
		ManagedInput{Completed: true, Message: "stage done"})
	return err
}

func TestToTaskStageIndexRewindsAndAddressesThatStage(t *testing.T) {
	ctx := context.Background()
	s, store, p := stageTargetFixture(t)
	a := child(t, s, p, "rework")
	approve(t, s, p)
	advanceToStage2(t, s, store, p, a.Task.ID, &stageSessionRunner{s: s, rootID: p.Task.RootID, taskID: a.Task.ID})

	// The stage-1 session recorded when it ran, versus the task's main session
	// (which the stage-2 turn has since overwritten).
	stage1, err := store.LatestStageRun(ctx, a.Task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetTask(ctx, a.Task.ID)
	if strings.TrimSpace(stage1.SessionKey) == "" {
		t.Fatal("fixture did not record a stage-1 session")
	}
	if stage1.SessionKey == before.MainSessionKey {
		t.Fatalf("fixture cannot distinguish stage 1 from the main session: %s", stage1.SessionKey)
	}

	// Sanity: while parked on stage 2, delivery would go to MainSessionKey --
	// stage 2's session -- not to stage 1's. That is the confusion being fixed.
	tmpl, err := s.TaskExecutionTemplate(before)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := taskMessageTarget(ctx, store, before, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if key == strings.TrimSpace(stage1.SessionKey) {
		t.Fatalf("fixture cannot distinguish the stages: both resolve to %s", key)
	}

	target := 1
	if _, err := s.ManagedAction(ctx, p.Task.RootID, a.Task.ID, "to-task",
		ManagedInput{Message: "the plan missed the retry path", StageIndex: &target}); err != nil {
		t.Fatal(err)
	}

	after, _ := store.GetTask(ctx, a.Task.ID)
	if after.CurrentStageIndex != 1 {
		t.Errorf("task did not rewind: stage=%d want 1", after.CurrentStageIndex)
	}
	if after.Status == StatusSuccess {
		t.Error("rewound task is still marked successful")
	}

	// The point of the feature: after the rewind, delivery resolves to the
	// addressed stage's own session rather than to MainSessionKey.
	key, chosen, err := taskMessageTarget(ctx, store, after, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if key != strings.TrimSpace(stage1.SessionKey) {
		t.Errorf("delivery key = %q, want stage 1's session %q", key, stage1.SessionKey)
	}
	if key == after.MainSessionKey {
		t.Errorf("delivery still resolves to MainSessionKey (%s)", key)
	}
	if chosen.Name != tmpl.Stages[1].Snapshot.Name {
		t.Errorf("chosen stage = %q, want %q", chosen.Name, tmpl.Stages[1].Snapshot.Name)
	}
}

func TestToTaskStageIndexRejectsBadTargets(t *testing.T) {
	ctx := context.Background()
	s, store, p := stageTargetFixture(t)
	a := child(t, s, p, "reject")
	approve(t, s, p)
	advanceToStage2(t, s, store, p, a.Task.ID, &stageSessionRunner{s: s, rootID: p.Task.RootID, taskID: a.Task.ID})

	for _, tc := range []struct {
		name  string
		stage int
		want  string
	}{
		{"ahead of current", 3, "ahead of the current stage"},
		{"negative", -1, "out of range"},
		{"past the template", 9, "out of range"},
		{"a user stage", 0, "only agent stages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.stage
			_, err := s.ManagedAction(ctx, p.Task.RootID, a.Task.ID, "to-task",
				ManagedInput{Message: "nope", StageIndex: &target})
			if err == nil {
				t.Fatalf("stage_index %d was accepted", tc.stage)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the rejection (want %q)", err, tc.want)
			}
			got, _ := store.GetTask(ctx, a.Task.ID)
			if got.CurrentStageIndex != 2 {
				t.Errorf("rejected message moved the task: stage=%d want 2", got.CurrentStageIndex)
			}
		})
	}
}

// Without StageIndex nothing changes: the message goes to the current stage.
func TestToTaskWithoutStageIndexKeepsCurrentBehaviour(t *testing.T) {
	ctx := context.Background()
	s, store, p := stageTargetFixture(t)
	a := child(t, s, p, "default")
	approve(t, s, p)
	advanceToStage2(t, s, store, p, a.Task.ID, &stageSessionRunner{s: s, rootID: p.Task.RootID, taskID: a.Task.ID})

	if _, err := s.ManagedAction(ctx, p.Task.RootID, a.Task.ID, "to-task",
		ManagedInput{Message: "continue here"}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetTask(ctx, a.Task.ID)
	if got.CurrentStageIndex != 2 {
		t.Errorf("message moved the task: stage=%d want 2", got.CurrentStageIndex)
	}
}

// The CLI forwards stdin JSON verbatim (cli/cmd/task_operations.go readTaskJSON
// keeps every key), and the HTTP handler decodes it straight into ManagedInput,
// so stage_index needs no CLI change. It must be a pointer: 0 is a valid stage
// index, and an absent field has to keep the legacy "current stage" behaviour.
func TestManagedInputStageIndexRoundTrip(t *testing.T) {
	var in ManagedInput
	if err := json.Unmarshal([]byte(`{"message":"x","stage_index":2}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.StageIndex == nil || *in.StageIndex != 2 {
		t.Fatalf("stage_index not decoded: %+v", in.StageIndex)
	}

	var plain ManagedInput
	if err := json.Unmarshal([]byte(`{"message":"x"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.StageIndex != nil {
		t.Fatalf("absent stage_index should be nil, got %v", *plain.StageIndex)
	}

	var zero ManagedInput
	if err := json.Unmarshal([]byte(`{"message":"x","stage_index":0}`), &zero); err != nil {
		t.Fatal(err)
	}
	if zero.StageIndex == nil || *zero.StageIndex != 0 {
		t.Fatalf("stage_index 0 lost: %+v", zero.StageIndex)
	}
}

// Delivery through the real path: deliverTaskMessages -> executeTaskMessages ->
// executeManagedTurn -> RunAgentStage. Asserting taskMessageTarget's return value
// is NOT enough -- executeManagedTurn reads MainSessionKey on a message turn and
// never consults taskMessageTarget, so the earlier assertions passed while the
// message still went to the wrong session.
type deliveryRecorder struct {
	groupTestRunner
	s      *Service
	rootID string
	taskID string
	// delivered records the session key each RunAgentStage was invoked with.
	delivered []string
}

func (r *deliveryRecorder) EnsureAgentSession(_ context.Context, exec AgentStageExecution) (string, error) {
	return fmt.Sprintf("session-stage-%d", exec.Run.StageIndex), nil
}

func (r *deliveryRecorder) RunAgentStage(ctx context.Context, exec AgentStageExecution) error {
	r.delivered = append(r.delivered, exec.Run.SessionKey)
	_, err := r.s.ManagedAction(ctx, r.rootID, r.taskID, "from-task",
		ManagedInput{Completed: true, Message: "stage done"})
	return err
}

func TestStageIndexDeliveryReachesTheAddressedStageSession(t *testing.T) {
	ctx := context.Background()
	s, store, p := stageTargetFixture(t)
	a := child(t, s, p, "delivery")
	approve(t, s, p)

	rec := &deliveryRecorder{s: s, rootID: p.Task.RootID, taskID: a.Task.ID}
	advanceToStage2(t, s, store, p, a.Task.ID, rec)

	stage1, err := store.LatestStageRun(ctx, a.Task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetTask(ctx, a.Task.ID)
	if stage1.SessionKey == before.MainSessionKey {
		t.Fatalf("fixture cannot distinguish stage 1 from the main session")
	}

	target := 1
	if _, err := s.ManagedAction(ctx, p.Task.RootID, a.Task.ID, "to-task",
		ManagedInput{Message: "address stage 1", StageIndex: &target}); err != nil {
		t.Fatal(err)
	}

	// Drive the message through the production delivery path.
	s.Runner = rec
	rec.delivered = nil
	if err := s.executeTaskMessages(ctx, p.Task.RootID, a.Task.ID); err != nil {
		t.Fatal(err)
	}
	s.Runner = nil
	if len(rec.delivered) == 0 {
		t.Fatal("message was never delivered")
	}
	got := rec.delivered[len(rec.delivered)-1]
	if got != strings.TrimSpace(stage1.SessionKey) {
		t.Errorf("delivered to %q, want the addressed stage's session %q (MainSessionKey is %q)",
			got, stage1.SessionKey, before.MainSessionKey)
	}
}

// An ordinary task keeps a single conversation, so addressing a stage is
// meaningless there. It must be rejected rather than silently ignored -- a
// caller that believes it chose a stage would otherwise see the message go to
// the only session and assume it worked.
func TestStageIndexRejectedForOrdinaryTasks(t *testing.T) {
	ctx := context.Background()
	s, _, p := stageTargetFixture(t)
	ordinary, err := s.CreateTask(ctx, CreateTaskInput{
		RootID:         p.Task.RootID,
		TaskTemplateID: p.Task.TaskTemplateID,
		Input:          "standalone work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Task.GroupID != "" {
		t.Fatalf("fixture task is grouped: %s", ordinary.Task.GroupID)
	}

	target := 0
	_, err = s.ManagedAction(ctx, p.Task.RootID, ordinary.Task.ID, "to-task",
		ManagedInput{Message: "address a stage", StageIndex: &target})
	if err == nil {
		t.Fatal("ordinary task accepted stage_index")
	}
	if !strings.Contains(err.Error(), "task group") {
		t.Errorf("error does not explain the rejection: %v", err)
	}

	// Without stage_index the ordinary path still works.
	if _, err := s.ManagedAction(ctx, p.Task.RootID, ordinary.Task.ID, "to-task",
		ManagedInput{Message: "plain message"}); err != nil {
		t.Fatalf("ordinary message rejected: %v", err)
	}
}

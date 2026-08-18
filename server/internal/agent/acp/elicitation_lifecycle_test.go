package acp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

// newTestProcess builds a Process with one registered session and a recording
// update handler, skipping the agent subprocess entirely.
func newTestProcess(t *testing.T, sessionKey, sessionID string) (*Process, *sessionState, *updateRecorder) {
	t.Helper()
	sess := &sessionState{ID: acpsdk.SessionId(sessionID)}
	rec := &updateRecorder{}
	sess.setOnUpdate(rec.record)
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{sessionKey: sess},
		sessionsByID: map[string]*sessionState{sessionID: sess},
		elicitation:  newElicitationRegistry(),
	}
	proc.setActiveSession(sessionID)
	return proc, sess, rec
}

type updateRecorder struct {
	mu      sync.Mutex
	updates []SessionUpdate
}

func (r *updateRecorder) record(update SessionUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, update)
}

func (r *updateRecorder) snapshot() []SessionUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SessionUpdate(nil), r.updates...)
}

// waitForPendingElicitation blocks until askUser has registered its entry.
func waitForPendingElicitation(t *testing.T, proc *Process) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		proc.elicitation.mu.Lock()
		for id := range proc.elicitation.pending {
			proc.elicitation.mu.Unlock()
			return id
		}
		proc.elicitation.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no elicitation was registered")
	return ""
}

func sampleQuestions() []types.AskUserQuestionItem {
	return []types.AskUserQuestionItem{{Question: "Proceed?", Options: []types.AskUserQuestionOption{{Label: "yes"}, {Label: "no"}}}}
}

// TestCancelCurrentTurnReleasesPendingElicitation covers the case where the
// user hits stop while an ask_user card is open: session/cancel does not
// cancel the SDK's inbound request context, so without an explicit release the
// agent's request would block for the full elicitationTimeout.
func TestCancelCurrentTurnReleasesPendingElicitation(t *testing.T) {
	proc, _, rec := newTestProcess(t, "key-1", "sess-1")

	done := make(chan error, 1)
	go func() {
		_, err := proc.askUser(context.Background(), "Proceed?", sampleQuestions())
		done <- err
	}()
	waitForPendingElicitation(t, proc)

	proc.releasePendingElicitations("key-1")

	select {
	case err := <-done:
		if !errors.Is(err, errElicitationDeclined) {
			t.Fatalf("askUser err = %v, want errElicitationDeclined", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("askUser did not return after the turn was cancelled")
	}

	if !hasAskUserCompletion(rec.snapshot()) {
		t.Fatal("cancelled elicitation did not emit a completing tool_call_update")
	}
}

// TestCloseSessionReleasesPendingElicitation covers forgetting the session
// (local release path) while a card is open.
func TestCloseSessionReleasesPendingElicitation(t *testing.T) {
	proc, _, _ := newTestProcess(t, "key-1", "sess-1")

	done := make(chan error, 1)
	go func() {
		_, err := proc.askUser(context.Background(), "Proceed?", sampleQuestions())
		done <- err
	}()
	waitForPendingElicitation(t, proc)

	proc.ForgetSession("key-1")

	select {
	case err := <-done:
		if !errors.Is(err, errElicitationDeclined) {
			t.Fatalf("askUser err = %v, want errElicitationDeclined", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("askUser did not return after the session was closed")
	}

	if got := proc.getActiveSession(); got != nil {
		t.Fatalf("getActiveSession = %#v, want nil after the only session closed", got)
	}
}

// TestAskUserAnswerFlowEmitsCompletionWithKind locks the happy path: the
// completion update must carry Kind=ask_user, because the kanban aux-flag hook
// keys off it to clear the task's "waiting for user" flag, and tool_call_update
// has no kind of its own.
func TestAskUserAnswerFlowEmitsCompletionWithKind(t *testing.T) {
	proc, _, rec := newTestProcess(t, "key-1", "sess-1")

	done := make(chan elicitationResult, 1)
	go func() {
		result, err := proc.askUser(context.Background(), "Proceed?", sampleQuestions())
		if err != nil {
			t.Errorf("askUser: %v", err)
		}
		done <- result
	}()
	id := waitForPendingElicitation(t, proc)

	if err := proc.answerElicitation(context.Background(), types.AskUserAnswer{
		ToolUseID: id,
		Answers:   map[string]string{"q_0": "yes"},
	}); err != nil {
		t.Fatalf("answerElicitation: %v", err)
	}

	select {
	case result := <-done:
		if result.answers["q_0"] != "yes" {
			t.Fatalf("answers = %#v", result.answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("askUser did not return after the answer was submitted")
	}

	var completion *acpsdk.SessionToolCallUpdate
	for _, update := range rec.snapshot() {
		if update.Type == UpdateTypeToolUpdate && update.Raw.ToolCallUpdate != nil {
			completion = update.Raw.ToolCallUpdate
		}
	}
	if completion == nil {
		t.Fatal("no tool_call_update was emitted")
	}
	if completion.Kind == nil || types.ToolKind(*completion.Kind) != types.ToolKindAskUser {
		t.Fatalf("completion.Kind = %v, want %q — the kanban waiting flag is never cleared without it", completion.Kind, types.ToolKindAskUser)
	}
	if completion.Status == nil || *completion.Status != acpsdk.ToolCallStatusCompleted {
		t.Fatalf("completion.Status = %v, want completed", completion.Status)
	}
}

func hasAskUserCompletion(updates []SessionUpdate) bool {
	for _, update := range updates {
		if update.Type == UpdateTypeToolUpdate && update.Raw.ToolCallUpdate != nil {
			return true
		}
	}
	return false
}

// TestAnswerRaceWithTimeoutIsNotLost covers the window where an answer is
// accepted (and recorded as answered in history) at the same instant the
// waiter's context is cancelled. Dropping it there would report an error to the
// agent for a question the user actually answered.
func TestAnswerRaceWithTimeoutIsNotLost(t *testing.T) {
	proc, _, _ := newTestProcess(t, "key-1", "sess-1")

	pending := &pendingElicitation{
		toolCallID: "elic_test_1",
		sessionID:  "sess-1",
		resultCh:   make(chan elicitationResult, 1),
		abandonCh:  make(chan struct{}),
	}
	proc.elicitation.register(pending)
	// An answer lands in the buffered channel just as the waiter gives up.
	pending.resultCh <- elicitationResult{toolCallID: pending.toolCallID, answers: map[string]string{"q_0": "yes"}}

	result, answered := proc.drainResolvedElicitation(pending, pending.toolCallID)
	if !answered {
		t.Fatal("an answer already in the channel was dropped on the timeout path")
	}
	if result.answers["q_0"] != "yes" {
		t.Fatalf("answers = %#v", result.answers)
	}
}

// TestElicitationIDsAreUniqueAcrossRegistries guards against the collision that
// follows an agent restart: ids are persisted as tool-call ids in session
// history, so a bare per-process counter would make a new question merge into
// an already-answered entry and render as un-answerable.
func TestElicitationIDsAreUniqueAcrossRegistries(t *testing.T) {
	first := newElicitationRegistry().nextToolCallID()
	second := newElicitationRegistry().nextToolCallID()
	if first == second {
		t.Fatalf("both registries produced %q; ids must survive an agent restart without colliding", first)
	}
}

// TestGetActiveSessionIgnoresStaleID covers a late session/update for a closed
// session: the routing target must not be left pointing at a session that can
// no longer receive anything.
func TestGetActiveSessionIgnoresStaleID(t *testing.T) {
	proc, sess, _ := newTestProcess(t, "key-1", "sess-1")
	proc.setActiveSession("sess-gone")

	if got := proc.getActiveSession(); got != sess {
		t.Fatalf("getActiveSession = %#v, want the one live session", got)
	}
}

// TestSessionUpdateIgnoresUnknownSession covers the poisoning path directly:
// a notification for an untracked session must not become the routing target.
func TestSessionUpdateIgnoresUnknownSession(t *testing.T) {
	proc, sess, _ := newTestProcess(t, "key-1", "sess-1")
	client := &mindfsClient{proc: proc}

	if err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId("sess-unknown"),
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content:       acpsdk.TextBlock("hi"),
				SessionUpdate: "agent_message_chunk",
			},
		},
	}); err != nil {
		t.Fatalf("SessionUpdate: %v", err)
	}

	proc.activeSession.mu.Lock()
	active := proc.activeSession.id
	proc.activeSession.mu.Unlock()
	if active != "sess-1" {
		t.Fatalf("activeSession = %q, want it untouched by an unknown session", active)
	}
	if got := proc.getActiveSession(); got != sess {
		t.Fatalf("getActiveSession = %#v, want the live session", got)
	}
}

// TestEmitSerializesConcurrentUpdates covers the data race between the SDK's
// per-request goroutines (elicitation, permission) and its single notification
// goroutine: the upper-layer handler keeps unsynchronized per-turn state, so
// delivery must be serialized per session. Run with -race to catch regressions.
func TestEmitSerializesConcurrentUpdates(t *testing.T) {
	sess := &sessionState{ID: acpsdk.SessionId("sess-1")}
	// Deliberately unsynchronized, mirroring the usecase handler's per-turn state.
	counter := 0
	sess.setOnUpdate(func(SessionUpdate) {
		counter++
	})

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				sess.emit(SessionUpdate{Type: UpdateTypeMessageChunk, SessionID: "sess-1"})
			}
		}()
	}
	wg.Wait()

	if counter != goroutines*perGoroutine {
		t.Fatalf("counter = %d, want %d — updates were delivered concurrently", counter, goroutines*perGoroutine)
	}
}

// TestEmitReportsMissingHandler locks the signal askUser relies on to detect
// that nothing can render the card.
func TestEmitReportsMissingHandler(t *testing.T) {
	sess := &sessionState{ID: acpsdk.SessionId("sess-1")}
	if sess.emit(SessionUpdate{Type: UpdateTypeMessageChunk}) {
		t.Fatal("emit reported delivery with no handler registered")
	}
	sess.setOnUpdate(func(SessionUpdate) {})
	if !sess.emit(SessionUpdate{Type: UpdateTypeMessageChunk}) {
		t.Fatal("emit reported no delivery with a handler registered")
	}
}

// TestAskUserWithoutHandlerDeclines covers the no-renderer case: it must be a
// protocol decline, not a JSON-RPC error the agent reads as a client failure.
func TestAskUserWithoutHandlerDeclines(t *testing.T) {
	sess := &sessionState{ID: acpsdk.SessionId("sess-1")}
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{"key-1": sess},
		sessionsByID: map[string]*sessionState{"sess-1": sess},
		elicitation:  newElicitationRegistry(),
	}
	proc.setActiveSession("sess-1")

	_, err := proc.askUser(context.Background(), "Proceed?", sampleQuestions())
	if !errors.Is(err, errElicitationDeclined) {
		t.Fatalf("askUser err = %v, want errElicitationDeclined", err)
	}
	proc.elicitation.mu.Lock()
	leftover := len(proc.elicitation.pending)
	proc.elicitation.mu.Unlock()
	if leftover != 0 {
		t.Fatalf("pending = %d, want the undeliverable entry cleaned up", leftover)
	}
}

// TestCreateElicitationDeclinesInsteadOfErroring locks the protocol mapping:
// a declined outcome must come back as the decline variant so the agent can
// unwind its turn gracefully.
func TestCreateElicitationDeclinesInsteadOfErroring(t *testing.T) {
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{},
		sessionsByID: map[string]*sessionState{},
		elicitation:  newElicitationRegistry(),
	}
	client := &mindfsClient{proc: proc}

	resp, err := client.UnstableCreateElicitation(context.Background(), acpsdk.UnstableCreateElicitationRequest{
		Form: &acpsdk.UnstableCreateElicitationForm{
			Message: "Proceed?",
			RequestedSchema: acpsdk.UnstableElicitationSchema{
				Properties: map[string]any{"go": map[string]any{"type": "boolean"}},
				Required:   []string{"go"},
			},
		},
	})
	if err != nil {
		t.Fatalf("UnstableCreateElicitation returned a JSON-RPC error instead of declining: %v", err)
	}
	if resp.Decline == nil {
		t.Fatalf("resp = %#v, want the decline variant", resp)
	}
}

// TestXAIAskUserQuestionSkipsInsteadOfErroring is the xAI-side counterpart:
// the runtime's SkipInterview outcome rather than a JSON-RPC error.
func TestXAIAskUserQuestionSkipsInsteadOfErroring(t *testing.T) {
	proc := &Process{
		agentName:    "test-agent",
		sessions:     map[string]*sessionState{},
		sessionsByID: map[string]*sessionState{},
		elicitation:  newElicitationRegistry(),
	}
	client := &mindfsClient{proc: proc}

	resp, err := client.handleXAIAskUserQuestion(context.Background(), []byte(`{"questions":[{"question":"Proceed?"}]}`))
	if err != nil {
		t.Fatalf("handleXAIAskUserQuestion returned an error instead of skipping: %v", err)
	}
	m, ok := resp.(map[string]any)
	if !ok || m["outcome"] != "skip_interview" {
		t.Fatalf("resp = %#v, want the skip_interview outcome", resp)
	}
}

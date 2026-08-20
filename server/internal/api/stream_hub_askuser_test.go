package api

import (
	"testing"

	agenttypes "mindfs/server/internal/agent/types"
)

// TestAskUserWaitingReflectsPendingAskUser 锁定「agent 提问等待回答」时 replying
// 接口应透出 askUserWaiting=true 的行为：合成 ask_user 工具调用（status=pending）
// 出现后，该 session 被判定为「需要用户输入」。
func TestAskUserWaitingReflectsPendingAskUser(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if !items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = false, want true after pending ask_user")
	}
}

// TestAskUserWaitingClearsAfterComplete 锁定「用户回答后」askUserWaiting 复位为
// false 的行为：complete 的 tool_call_update 追加后倒序扫描应判定已离开提问态。
func TestAskUserWaitingClearsAfterComplete(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
		},
	})
	complete := "complete"
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolUpdate),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: complete,
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = true after ask_user completed, want false")
	}
}

// TestAskUserWaitingIgnoresLaterAssistantChunks 锁定「回答后 agent 继续输出」的
// 场景：末尾是 message_chunk，倒序扫描应越过它、停在 complete 的 ask_user，仍为
// false。若误判为 true，Android 端会一直显示「需要你输入」。
func TestAskUserWaitingIgnoresLaterAssistantChunks(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
		},
	})
	complete := "complete"
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolUpdate),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: complete,
		},
	})
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeMessageChunk),
		Data: agenttypes.MessageChunk{Content: "继续干活"},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = true after subsequent assistant output, want false")
	}
}

// TestAskUserWaitingOnlyForActiveSession 锁定「session 未 active 不进入列表」的
// 既有行为，确认新字段不破坏它。
func TestAskUserWaitingOnlyForActiveSession(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.AppendReplyEvent("orphan-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 0 {
		t.Fatalf("replying sessions = %d, want 0 for non-active session", len(items))
	}
}

// TestAskUserWaitingSetsRootID 用 SetPendingReply 建立 active session 后确认
// ListReplyingSessions 带出 rootId（供 AskUserWaiting 判定依赖 active + rootID）。
func TestAskUserWaitingSetsRootID(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if items[0].RootID != "root-1" || items[0].SessionKey != "sess-1" {
		t.Fatalf("unexpected identity: rootID=%q sessionKey=%q", items[0].RootID, items[0].SessionKey)
	}
	if items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = true with no ask_user events, want false")
	}
}

// TestAskUserQuestionTextFormatsQuestions 锁定等待输入时 replying 接口透出的
// askUserQuestion 是「问题 + 选项」的多行文本。
func TestAskUserQuestionTextFormatsQuestions(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
			Meta: map[string]any{
				"questions": []agenttypes.AskUserQuestionItem{
					{
						Question: "要部署到哪个平台？",
						Header:   "部署平台确认",
						Options: []agenttypes.AskUserQuestionOption{
							{Label: "Android"},
							{Label: "iOS"},
						},
					},
				},
			},
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if !items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = false, want true")
	}
	want := "要部署到哪个平台？\n• Android\n• iOS"
	if items[0].AskUserQuestion != want {
		t.Fatalf("AskUserQuestion = %q, want %q", items[0].AskUserQuestion, want)
	}
}

// TestAskUserQuestionTextEmptyWhenAnswered 锁定已回答后 askUserQuestion 复位为空。
func TestAskUserQuestionTextEmptyWhenAnswered(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolCall),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
			Meta: map[string]any{
				"questions": []agenttypes.AskUserQuestionItem{
					{Question: "要部署到哪个平台？", Options: []agenttypes.AskUserQuestionOption{{Label: "Android"}}},
				},
			},
		},
	})
	complete := "complete"
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: string(agenttypes.EventTypeToolUpdate),
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: complete,
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = true after answered, want false")
	}
	if items[0].AskUserQuestion != "" {
		t.Fatalf("AskUserQuestion = %q after answered, want empty", items[0].AskUserQuestion)
	}
}

// TestAskUserWaitingClearsAfterCompleteRealType 锁定真实链路的 complete 事件类型
// 也能让 askUserWaiting 归位：updateToEvent（ws.go）把 EventTypeToolUpdate 落库成
// "tool_call_update"，而 agenttypes.EventTypeToolUpdate 常量值是 "tool_update"。
// 若 askUserWaiting 只认常量，会跳过这条 complete、永远停在 pending。历史回归：
// claude/codex 回答后补发 complete，但 Android 通知一直显示「需要你输入」。
func TestAskUserWaitingClearsAfterCompleteRealType(t *testing.T) {
	hub := NewStreamHub(nil)
	hub.SetPendingReply("root-1", "sess-1", "会话一")

	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: "tool_call",
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "pending",
		},
	})
	// 模拟真实链路：complete 的 tool_update 事件类型是 "tool_call_update"。
	hub.AppendReplyEvent("sess-1", StreamEvent{
		Type: "tool_call_update",
		Data: agenttypes.ToolCall{
			CallID: "elic_abc_1",
			Kind:   agenttypes.ToolKindAskUser,
			Status: "complete",
		},
	})

	items := hub.ListReplyingSessions()
	if len(items) != 1 {
		t.Fatalf("replying sessions = %d, want 1", len(items))
	}
	if items[0].AskUserWaiting {
		t.Fatalf("AskUserWaiting = true after %q complete, want false", "tool_call_update")
	}
	if items[0].AskUserQuestion != "" {
		t.Fatalf("AskUserQuestion = %q after answered, want empty", items[0].AskUserQuestion)
	}
}

// TestAskUserQuestionTextFromAnyJSON 锁定 JSON 化的 questions（[]map[string]any）
// 也能被宽松解析兜底，防止 WS 重放/历史恢复丢字段。
func TestAskUserQuestionTextFromAnyJSON(t *testing.T) {
	got := askUserQuestionTextFromAny([]any{
		map[string]any{
			"question": "Q1",
			"options": []any{
				map[string]any{"label": "A"},
				map[string]any{"label": "B"},
			},
		},
	})
	want := "Q1\n• A\n• B"
	if got != want {
		t.Fatalf("askUserQuestionTextFromAny = %q, want %q", got, want)
	}
}

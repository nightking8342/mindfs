package usecase

import (
	"context"
	"testing"

	agenttypes "mindfs/server/internal/agent/types"
	rootfs "mindfs/server/internal/fs"
	"mindfs/server/internal/session"
)

// newLateRouteRouter 造一个绑定到父会话的 claude 子代理路由器，并返回
// 「投递一条流式块」的助手函数与创建出的子会话指针。
//
// 构造方式与既有用例 TestClaudeSubagentRouterCreatesChildSessionAndRoutesChunks 一致，
// 保证本用例失败时排除「测试脚手架写错」这一可能。
func newLateRouteRouter(t *testing.T) (*claudeSubagentRouter, func(text string) agenttypes.Event, **session.Session) {
	t.Helper()
	ctx := context.Background()
	root := rootfs.NewRootInfo("mindfs", "mindfs", t.TempDir())
	manager := session.NewManager(root)
	parent, err := manager.Create(ctx, session.CreateInput{Type: session.TypeChat, Agent: "claude", Name: "parent"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	created := new(*session.Session)
	router := newClaudeSubagentRouter(subagentSessionInput{
		RootID:  root.ID,
		Parent:  parent,
		Agent:   "claude",
		Model:   "sonnet",
		Mode:    "default",
		Effort:  "medium",
		Manager: manager,
		OnCreated: func(child *session.Session) {
			*created = child
		},
	})

	// 子代理的流式块只带 ParentToolUseID/TaskID（这正是真实 SDK 的行为：
	// subagentMetaFromPartial 只填 ParentToolUseID），所以归属完全依赖
	// find(ref) 与 child.closed 两个判定。
	chunk := func(text string) agenttypes.Event {
		return agenttypes.Event{
			Type: agenttypes.EventTypeMessageChunk,
			Data: agenttypes.MessageChunk{
				Content:         text,
				ParentToolUseID: "tool-1",
				TaskID:          "task-1",
			},
		}
	}
	return router, chunk, created
}

// TestClaudeSubagentLateChunkStillRoutesToChild 固化「主 turn 结束后，后台子代理
// 继续产生的流式输出仍必须路由到子会话（而非落回主会话）」这一契约。
//
// 背景（上游缺陷）：claude 的**后台** Task 子代理不随主 turn 结束而停止 —— 主 turn 的
// ResultMessage 先到，子代理仍在跑。但 `SendMessage` 在返回前会调用
// `claudeSubagents.FinishAll()`（session.go:2435），把**所有** child（不区分前台/后台）
// 标成 `closed=true`：
//
//	func (r *claudeSubagentRouter) FinishAll() {
//	    for _, child := range r.children {
//	        if child == nil || child.closed { continue }
//	        child.done()
//	        child.closed = true          // ← 关掉所有子代理
//	    }
//	}
//
// 而 `Handle` 的第二道门槛是 `if child == nil || child.closed { return false }`
// （session.go:2644）。`closed` 全仓**只被置 true、从未被重置回 false**，因此
// FinishAll 之后，后台子代理的每一条流式块都被 `Handle` 判为「不消费」。
//
// 调用方 `attachSessionUpdates` 拿到 false 后**继续走主会话分支**，最终
// `in.OnUpdate(clientUpdate)` → `BroadcastSessionUpdate(rootID, 主会话key, ...)`。
// 前端按 `session_key` 渲染，于是子代理的输出一条条出现在**主会话窗口**里
// ——用户描述的现象。广播层与前端都没有过错，是路由兜底把事件交还给了主会话。
//
// 本测试断言第 3 步（FinishAll 之后）仍必须被消费。
func TestClaudeSubagentLateChunkStillRoutesToChild(t *testing.T) {
	ctx := context.Background()
	router, chunk, created := newLateRouteRouter(t)

	// 1) 主 turn 进行中：子代理流式块到达，建立并路由到子会话。
	if consumed := router.Handle(ctx, chunk("during parent turn")); !consumed {
		t.Fatalf("主 turn 进行中的子代理块未被消费（前置条件不成立）")
	}
	if *created == nil {
		t.Fatalf("子会话未建立（前置条件不成立）")
	}
	childKey := (*created).Key

	// 2) 主 turn 结束 —— 复刻 session.go:2435 的行为。
	//    后台子代理此刻**仍在运行**（claude 不阻塞等待后台 Task）。
	router.FinishAll()

	// 3) 后台子代理继续吐流式块。期望：仍被识别为子代理、路由到子会话。
	if consumed := router.Handle(ctx, chunk("late background output")); !consumed {
		t.Fatalf(
			"主 turn 结束后，后台子代理的流式块未被消费 —— 它会落回主会话分支，"+
				"并以主会话 key（而非子会话 key %q）广播，表现为子代理输出串进主会话窗口",
			childKey,
		)
	}
}

// TestClaudeSubagentFinishAllClosesAllChildren 固化 FinishAll 的**当前**行为，
// 作为上一条的对照证据：它确实关掉了所有 child（含仍在后台运行的），
// 这正是上一条失败的机制来源。
//
// 若日后 FinishAll 改成「只关前台子代理」，本测试会失败——那时应当一并更新它，
// 并确认 TestClaudeSubagentLateChunkStillRoutesToChild 仍为绿。
func TestClaudeSubagentFinishAllClosesAllChildren(t *testing.T) {
	ctx := context.Background()
	router, chunk, created := newLateRouteRouter(t)

	if consumed := router.Handle(ctx, chunk("during parent turn")); !consumed {
		t.Fatalf("前置条件不成立：子代理块未被消费")
	}
	if *created == nil {
		t.Fatalf("前置条件不成立：子会话未建立")
	}

	router.FinishAll()

	stillOpen := 0
	for _, child := range router.children {
		if child != nil && !child.closed {
			stillOpen++
		}
	}

	if stillOpen != 0 {
		t.Fatalf("FinishAll 之后仍有 %d 个子代理未关闭；机制与预期不符", stillOpen)
	}
}

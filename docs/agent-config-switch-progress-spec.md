# Agent 配置切换进度展示规格

**状态**：已定稿，按此实现
**范围**：配置备份切换（`POST /api/agent-config/switch`）的过程可见性
**归属**：fork 功能（实现后登记 `FORK.md`）

---

## 1. 背景与目标

### 1.1 现状问题

切换一个配置备份时，后端依次做 7 件事：恢复配置文件 → 写入独立 Claude settings → 应用环境变量（agents.json / pool / prober 三处）→ 关闭该 agent 的全部会话 → 记录 last selection → 启动探活。

但用户只看到弹窗关闭，没有任何反馈。具体问题：

- **过程不可见**：做了什么、恢复了几个文件、应用了几项 env，全都不知道。
- **探活完全异步**：`triggerAgentConfigSwitchProbe` 起 goroutine（超时 4 分钟），HTTP 在它启动后立即返回。agent 何时真正可用，前端无从得知。
- **跨端断线无解释**：`KillAgentProcess` 关闭该 agent 的**所有**会话，不区分项目、不区分客户端。在手机上切换配置，桌面端的会话会莫名断开。
- **失败无法定位**：`switchAgentConfig` 中途出错就 `return err`，前端只拿到一条消息，不知道卡在哪一步、哪些步骤已经执行。

### 1.2 目标

1. 切换后展示**步骤结果清单**，带真实详情（文件数、env 项数、目标 agent）。
2. 探活作为唯一的**实时进行项**，可后台运行，弹窗可随时关闭且进度不丢。
3. 探活失败时给出原因和**重试探活**入口。
4. 其它设备能知道会话为何被重启。

### 1.3 非目标

- **不为前 6 步做逐步动画**。这 6 步是文件拷贝加几个 JSON 写入，总耗时毫秒级，逐步点亮的观感是假的。它们一次性呈现为已完成清单。
- 不做切换历史记录（失败原因只在当次可见）。
- 不做回滚到上一个配置。
- 不接 API 供应商切换的 UI（后端广播能力自动覆盖它，前端本次不做）。
- 不做断线重连的进度补推（用超时 + 主动拉取兜底，见 §6）。

---

## 2. 关键技术前提

**`agent.status.changed` 事件不可靠，不能作为探活完成信号。**

`Prober.setStatus` 中有过滤（`probe.go`）：

```go
if hadPrev && !statusChanged(prev, status) {
    return   // 状态没变化，不通知 listener
}
```

`statusChanged` 逐字段比较 available / error / models / efforts 等。切换两个都正常、模型列表又相同的配置（例如同一网关下只改了 max tokens 的两个备份），探活后所有字段一致 → 事件不推送 → 前端永远停在「探活中」。

因此本设计新增一条**无条件广播** `agent.config.switched`，它不经过 `statusChanged` 过滤。这既是可靠的完成信号，也顺带解决了跨端断线无解释的问题。

---

## 3. 架构与数据流

```
用户点「切换」
      │
      ├─ POST /api/agent-config/switch ────────────────┐
      │                                               │
      │   同步执行 6 步，逐步计时记录：                  │
      │     restore_files    count: 2                 │
      │     claude_settings  target: <P>              │
      │     apply_env        count: 3                 │
      │     kill_sessions    target: claude           │
      │     record_selection                          │
      │   启动探活 goroutine，立即返回                  │
      │                                               │
      └─ 响应 { needs_confirm, backup, steps[] } ◄─────┘
              │
              ├─ 渲染步骤清单（真实详情，标记为已完成）
              └─ probe 项标记 running，本地起计时器
                       │
      探活 goroutine 结束（几秒~几十秒）
                       │
      ├─ 广播 agent.config.switched   ← 无条件，可靠完成信号
      │     { agent, backup_id, backup_name, available, error }
      │        │
      │        ├─ 发起端：probe → ok / failed + 原因
      │        └─ 其它端：提示「配置已切换为 X，会话已重启」
      │
      └─ 顺带触发已有的 agent.status.changed（仅当状态真变了）
            └─ 现有行为不动：setAgentsVersion(v+1) 刷新列表
```

---

## 4. 后端改动

全部位于 `server/internal/api/`，FORK.md 已登记该文件归我方。

### 4.1 步骤记录类型

```go
// agentConfigSwitchStep records one stage of a config switch for the UI.
type agentConfigSwitchStep struct {
    Key        string `json:"key"`
    Status     string `json:"status"`
    Count      int    `json:"count,omitempty"`
    Target     string `json:"target,omitempty"`
    DurationMS int64  `json:"duration_ms"`
    Error      string `json:"error,omitempty"`
}
```

`Key` 取值：`restore_files`、`claude_settings`、`apply_env`、`kill_sessions`、`record_selection`、`probe`。

`Status` 取值：`ok`、`failed`、`running`、`skipped`。

`Count` 用于文件数与 env 项数；`Target` 用于 agent 名或独立 settings 路径。

**不返回拼好的中文文案**：前端按 `Key` 查 i18n，用 `Count` / `Target` 填参数。（现有 `needs_confirm` 分支返回中文 message 属既有问题，本次不扩大，也不顺手修改。）

### 4.2 `switchAgentConfig` 返回值

```go
type agentConfigSwitchResult struct {
    Entry        agentConfigManifestEntry  `json:"backup"`
    NeedsConfirm bool                      `json:"needs_confirm"`
    Steps        []agentConfigSwitchStep   `json:"steps,omitempty"`
}

func switchAgentConfig(req agentConfigSwitchRequest, app *AppContext) (agentConfigSwitchResult, error)
```

由 3 个返回值改为结构体。需同步更新 6 处测试调用，属机械改动。

**取舍**：加第 4 个返回值更省 diff，但函数体内本来就要插入一批步骤记录调用，diff 已经不小，此处取可读性。

### 4.3 步骤记录器

```go
type switchStepRecorder struct {
    steps []agentConfigSwitchStep
}

func (r *switchStepRecorder) ok(key string, start time.Time, count int, target string)
func (r *switchStepRecorder) fail(key string, start time.Time, err error)
func (r *switchStepRecorder) skip(key string)
func (r *switchStepRecorder) running(key string)
```

在 `switchAgentConfig` 现有代码中插入调用，不重构其控制流。

`skipped` 用于：非 isolated 备份跳过 `claude_settings`；备份无 env 时跳过 `apply_env`。

### 4.4 probe 步骤由后端追加

成功启动探活后，后端在 `Steps` 末尾追加一项：

```go
{Key: "probe", Status: "running", DurationMS: 0}
```

前端直接渲染 `steps` 即可，无需自行拼接探活项。

**中途失败时不追加该项** —— 探活根本没启动，列表止于失败步骤。

### 4.5 失败路径也返回 steps

现状是中途出错直接 `return err`。改为：错误返回时 `agentConfigSwitchResult.Steps` 仍带上已完成的步骤与失败的那一步（`Status: "failed"`，`Error` 非空）。

HTTP 层在 `err != nil` 时仍返回 400，但响应体带 `steps`，供前端定位失败点。

### 4.6 完成广播

`triggerAgentConfigSwitchProbe` 增加两个参数（`backupID`、`backupName`），在 goroutine 末尾无条件广播：

```go
status := prober.ProbeOne(ctx, agentName)
notifier.AgentConfigSwitched(agentConfigSwitchedEvent{
    Agent:      agentName,
    BackupID:   backupID,
    BackupName: backupName,
    Available:  status.Available,
    Error:      firstNonEmpty(status.Error, status.ProbeError),
})
```

事件类型 `agent.config.switched`，经 `AppContext.GetSessionStreamHub().BroadcastAll` 发出。

该函数另有两个调用方 —— `restartAgent`（手动重启 / 重试探活）与 `switchAgentAPIProvider`（`agent_api_provider.go`）。它们传空的 `backupID` / `backupName`，前端按 agent 名匹配。**这两条路径因此自动获得完成信号**，本次仅接配置切换的前端 UI。

### 4.7 广播的可测性

`BroadcastAll` 依赖 `SessionStreamHub`，现有测试均传 `app == nil`，无法验证广播是否发出。

引入最小接口隔离：

```go
type agentConfigSwitchNotifier interface {
    AgentConfigSwitched(evt agentConfigSwitchedEvent)
}
```

`AppContext` 实现它；测试注入 fake 记录调用。

**理由**：「广播没发出」是本设计中最难靠肉眼发现的失败模式 —— 前端会永远停在探活中，而后端日志一切正常。这个失败模式值得自动化覆盖。

---

## 5. 前端改动

### 5.1 `services/agentConfig.ts`

```ts
export type AgentConfigSwitchStep = {
  key: string;
  status: "ok" | "failed" | "running" | "skipped";
  count?: number;
  target?: string;
  duration_ms: number;
  error?: string;
};
```

`switchAgentConfig` 返回值加 `steps?: AgentConfigSwitchStep[]`。

### 5.2 全局进度 state

放在 `App.tsx`（仿现有的 `gitHubImportState`）：

```ts
type AgentConfigSwitchProgress = {
  agent: string;
  backupID: string;
  backupName: string;
  steps: AgentConfigSwitchStep[];
  probe: "running" | "ok" | "failed" | "unknown";
  probeError: string;
  startedAt: number;
  finishedAt: number;
};
```

**取舍**：放 App.tsx 而非 FileTree 内部，因为关掉弹窗后进度必须存活，而弹窗开关状态就在 FileTree 里；放全局还能让「其它设备切换了配置」的提示复用同一份数据。代价是 FileTree 多几个 props。

`probe: "unknown"` 表示前端超时，含义是「未在 90 秒内收到结果，可能仍在进行」，**不等同于失败**。

### 5.3 WS 事件处理

`App.tsx` 的事件 switch 中新增 `case "agent.config.switched"`：

- payload 的 agent 与本端 running 进度匹配 → 更新 `probe` 为 `ok` / `failed`，填 `probeError`，记 `finishedAt`。
- 不匹配（即其它设备发起的切换）→ 经 `errorService` 发一条 `severity: "info"` 的提示「配置已切换为 X，会话已重启」，解释本端会话为何断开。（`ErrorSeverity` 已含 `"info"`，`ToastContainer` 只过滤 `fatal`，info 级会显示 3 秒。）

现有 `case "agent.status.changed"` 的处理（`setAgentsVersion(v + 1)`）保持不变。

### 5.4 弹窗新增 `switching` 步骤

`AgentConfigStep` 增加 `"switching"`。`runAgentConfigSwitch` 成功后不再直接 `closeAgentConfigFlow()`，改为写入进度 state 并 `setAgentConfigStep("switching")`。

渲染内容：步骤清单 + 探活行（秒表或结果）+ 按钮：

| 探活状态 | 按钮 |
|---|---|
| `running` | `[后台运行]`（关弹窗、保留进度） |
| `ok` | `[关闭]` |
| `failed` / `unknown` | `[重试探活]`、`[关闭]` |

### 5.5 agent 列表标记

复用 `AgentMenuList` 已有的 `renderEnd` 插槽（当前用于显示上次选中的配置名）。当进度为 running 且弹窗已关闭时，该 agent 行右侧显示「● cpa 切换中…」，点击回到进度。不新增 UI 位置。

### 5.6 重试探活

调用已有的 `restartAgent(agent)`（`services/agents.ts`，对应 `POST /api/agents/restart`）。该接口内部即 `KillAgentProcess` + `triggerAgentConfigSwitchProbe`。

重试后将 `probe` 置回 `running`、重置计时器，等待新的 `agent.config.switched`。

### 5.7 i18n

`zh-CN` / `en-US` 各新增：

- 6 个步骤名，`restore_files` 与 `apply_env` 带 `{count}` 参数，`kill_sessions` 与 `claude_settings` 带 `{target}` 参数
- 探活状态文案（进行中 / 就绪 / 失败 / 未知）
- 按钮文案（后台运行 / 重试探活）
- 半切换警告文案、跨端切换提示文案

---

## 6. 错误处理与边界

| 场景 | 处理 |
|---|---|
| 切换中途失败 | 返回已完成 steps + 失败步骤，HTTP 400。前端显示到失败点，不进入探活。**明确提示配置处于中间态**（例如文件已恢复但 env 未应用），文案建议重新切换一次。含糊其辞比说清楚更危险 |
| `needs_confirm` | 第一次请求不执行任何步骤，`steps` 为空。走现有确认流程，第二次请求才有 steps |
| 探活失败 | 配置已生效、agent 不可用。显示原因 + `[重试探活]` |
| 前端 90 秒超时 | `probe → unknown`，文案说明「未在 90 秒内返回，可能仍在进行」，不谎称失败。给重试入口 |
| WS 断线丢广播 | 双保险：① 重连时若存在 running 进度，主动 `fetchAgents(true)`，用 `available` 判定结果；② 90 秒超时兜底 |
| 切换进行中又点切换 | 进度为 running 时禁用切换按钮，避免并发切换同一 agent |
| 同时切换两个 agent | 进度 state 只存一个，新的覆盖旧的。同时切两个 agent 的配置属极罕见操作，不为它增加复杂度 |
| 服务端重启 | 进度丢失，走 90 秒超时兜底 |

---

## 7. 测试

### 7.1 后端

新增用例，与现有 `agent_config_edit_test.go` 同风格（`setupAgentConfigTest` 隔离 HOME / 配置目录 / agents.json）：

| # | 场景 | 断言 |
|---|---|---|
| 1 | 成功切换（2 个文件 + 3 项 env） | 6 个步骤的 `key` / `status` 顺序正确；`restore_files.count == 2`；`apply_env.count == 3`；`kill_sessions.target == "claude"` |
| 2 | 非 isolated 备份 | `claude_settings` 步骤 `status == "skipped"` |
| 3 | 无 env 的备份 | `apply_env` 步骤 `status == "skipped"` |
| 4 | 中途失败（来源快照缺失） | 返回 error，且 `Steps` 含已完成步骤与一个 `status == "failed"`、`Error` 非空的步骤 |
| 5 | `needs_confirm` | `Steps` 为空 |
| 6 | 探活结束触发广播 | 注入 fake notifier，断言 `AgentConfigSwitched` 被调用一次，`Available` 与 `Error` 与探活结果一致 |

### 7.2 前端

项目无前端测试框架（`tsc --noEmit` 是唯一门槛，见 CLAUDE.md）。前端靠类型检查 + 部署后手动验证：

1. 正常切换 → 步骤清单显示真实数字 → 探活转圈 → 就绪
2. 切换到不可用网关 → 探活失败 → 显示原因 → 重试探活
3. 探活期间关闭弹窗 → agent 行显示「切换中…」→ 点开回到进度
4. 另一台设备切换配置 → 本端收到提示

---

## 8. 决策摘要

| 议题 | 结论 | 理由 |
|---|---|---|
| 前 6 步是否做逐步动画 | **否**，一次性呈现为结果清单 | 毫秒级操作，动画是假的 |
| 探活完成信号 | **新增无条件广播** `agent.config.switched` | `agent.status.changed` 被 `statusChanged` 过滤，状态没变时不推 |
| 进度状态存放 | 前端 App.tsx 全局 state | 关弹窗不丢；无需后端存储 |
| 多端同步 | 仅广播完成事件，不同步中间进度 | `KillAgentProcess` 影响所有端，需解释断线；中间进度对旁观端无价值 |
| 重试探活 | 复用 `POST /api/agents/restart` | 该接口行为恰好等价，无需新端点 |
| 失败时是否回滚 | **否** | 需记录上一配置并重跑完整恢复，复杂度高；重试探活覆盖绝大多数场景 |
| 切换历史 | **否** | 本次 YAGNI |
| API 供应商切换 UI | **本次不接** | 后端广播自动覆盖，前端可后续复用 |
| 广播的测试 | **引入可注入接口** | 「广播不发」会让前端永远卡住，且后端日志正常，最难肉眼发现 |

---

## 9. FORK 登记（实现合并时）

| 范围 | 内容 | 归属 |
|---|---|---|
| `server/internal/api/agent_config.go` | 步骤记录、`switchAgentConfig` 返回结构体、完成广播 | 我方 |
| `server/internal/api/appcontext.go` | `AgentConfigSwitched` 广播方法 | 我方 |
| `server/internal/api/agent_api_provider.go` | `triggerAgentConfigSwitchProbe` 调用处加参数 | 我方 |
| `web/src/App.tsx` | 进度 state、`agent.config.switched` 事件处理 | 我方 |
| `web/src/components/FileTree.tsx` | `switching` 步骤 UI、`renderEnd` 标记 | 我方 |
| `web/src/services/agentConfig.ts` | `AgentConfigSwitchStep` 类型 | 我方 |
| `docs/agent-config-switch-progress-spec.md` | 本规格 | 我方 |

---

## 10. 修订记录

| 日期 | 说明 |
|---|---|
| 2026-07-30 | 初稿定稿，对应会话中的设计讨论结论 |

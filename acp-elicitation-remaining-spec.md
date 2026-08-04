# ACP elicitation 中低危问题修复规格

对象：`D:\claudebot\mindfs`（Go 1.25 + React）。全部改动集中在
`server/internal/agent/acp/`，只有 A 项会碰 `server/internal/session/`。

高危问题（数据竞争、取消不释放、Kind 缺失、activeSession 污染、多 session 路由）
**已经修完并有测试**，本规格只覆盖剩余 5 项。不要改动已有的 `emit` / `emitMu` /
`abandonSession` / `clearActiveSession` / `errElicitationDeclined` /
`drainResolvedElicitation` 机制。

## 通用要求

- 每项都要有单元测试，放在 `server/internal/agent/acp/` 下。
- 测试必须"能抓到 bug"：写完后把对应修复临时回退，确认测试真的失败，再恢复。
- 门槛：`go build ./...`、`go vet ./server/internal/agent/acp/...`、
  `go test ./server/internal/agent/acp/... ./server/internal/session/...` 全绿。
- Go 不在 PATH，先 `export PATH="/c/Go/bin:$PATH"`。
- 注释写"约束/为什么"，不写"这行在干什么"。风格对齐周围代码。中文注释可以，
  但现有 acp 包注释是英文，保持英文。
- 不要动 `web/` 前端。所有修复都必须在后端闭环。

---

## A. 隔离 agent 提供的 `_meta`（最重要的一项）

### 现状与风险

`acp/session.go` 的 `convertEvent` 现在把 agent 的 `_meta` 原样搬进
`types.ToolCall.Meta`（两处：`raw.ToolCall.Meta` 与 `raw.ToolCallUpdate.Meta`）。
这个 map 直接落进 MindFS 的保留键空间，而这些键是有语义的：

- `meta.questions` / `meta.answers` — `web/src/components/SessionViewer.tsx:549,566`
  用它渲染提问卡片。伪造 `answers` 会让卡片显示成"已提交"、用户点不动；
  伪造 `questions` 能把任意工具调用变成提问卡片，提交时报"no pending elicitation"。
- `meta.title` — `SessionViewer.tsx:671` 覆盖显示标题。
- `meta.source` == `"userShell"` + `meta.phase` == `"stream"` —
  `server/internal/api/stream_hub.go:651,660` 据此把事件并进 shell 合并路径。
- `meta.source`/`phase`/`shell` — `server/internal/session/types.go:134,153-155`。
- `meta.filePath` / `meta.path` — `usecase/session.go:3677-3681` 会做路径规范化。
- `meta.output` — `SessionViewer.tsx:372`。

另外 `session/types.go:91` 的 `compactToolCallMeta` 只剔除 `"output"` 一个键，
其余原样持久化进会话 JSONL，且 ask_user 类型走
`PreserveToolCallContent` 分支**完全不压缩**，等于 agent 可以把任意大小的
blob 写进历史文件并在回放时重新广播。

### 要做的事

**A1. 区分"可信 meta"与"agent meta"。**

`acp/process.go` 的内部类型 `SessionUpdate` 增加一个字段：

```go
type SessionUpdate struct {
	Type      UpdateType
	SessionID string
	Raw       acp.SessionUpdate
	// TrustedMeta carries meta that MindFS itself produced (never parsed from
	// the agent's wire payload), so it is exempt from sanitizing. Forgery is
	// impossible because this field has no JSON representation on the wire.
	TrustedMeta map[string]any
}
```

关键点：`SessionUpdate` 是 MindFS 内部结构，**永远不会**从 agent 的 JSON 反序列化
得到，所以 agent 无法伪造 `TrustedMeta`。而 `Raw` 里的 `Meta` 完全由 agent 控制。

**A2. `acp/elicitation.go` 里 fork 自己合成的两处改用 `TrustedMeta`。**

- `askUser` 中合成的 ask_user tool call：现在往
  `acp.SessionUpdateToolCall.Meta` 塞 `{"questions":..., "title":...}`，
  改为塞进 `SessionUpdate.TrustedMeta`，`Raw` 里的 `Meta` 留空。
- `toolCallCompleteUpdate` 中的 `{"answers":...}` 同样改走 `TrustedMeta`。

注意 `toolCallCompleteUpdate` 的 `Kind` 必须继续设置（看板清"等待用户"标志靠它），
已有测试 `TestAskUserAnswerFlowEmitsCompletionWithKind` 会守住这点，别破坏它。

**A3. `convertEvent` 做净化 + 合并。**

两处 `Meta:` 赋值改为 `Meta: mergeToolCallMeta(update.TrustedMeta, <raw meta>)`。
新函数语义：

1. 先取 agent meta，逐键过滤掉 MindFS 保留键（下面的清单），保留其余键。
2. 再把 `TrustedMeta` 的键覆盖上去（可信优先）。
3. 两者都为空时返回 `nil`，不要返回空 map（避免下游 `Meta != nil` 判断变化）。

保留键清单（被丢弃的 agent 键），定义成包级 `var` 便于测试引用：

```
questions, answers, title, source, phase, shell, output, input,
filePath, path, replayTruncated, replayTruncation, source_session,
toolUseId, parentToolUseId, cancelled, exitCode, error, taskTool
```

被丢弃时打一条日志（`log.Printf`，带 agent 名、tool call id、被丢的键名列表，
**不要打值**），方便日后发现某个 agent 确实需要某个键。

**A4. 限制 agent meta 的体积。**

净化后的 agent meta（不含 `TrustedMeta`）序列化成 JSON 后若超过 **8 KiB**，
整体丢弃并记一条日志。理由：ask_user / edit 等类型走
`PreserveToolCallContent` 不压缩，会原样持久化进 JSONL 并在回放时重播。
用 `json.Marshal` 估算即可，失败（不可序列化）也按丢弃处理。

**A5.** `server/internal/session/types.go` 的 `compactToolCallMeta` 不要改。
体积控制在 A4 的入口处做，改压缩函数会影响 claude/codex 两条既有链路。

### 测试要求

- agent 伪造 `answers`/`questions`/`source`/`phase` 时，`convertEvent` 产出的
  `Meta` 里不含这些键。
- fork 自己经 `TrustedMeta` 传的 `questions`/`title`/`answers` **必须**保留
  （否则提问卡片直接坏掉——这是最容易改错的地方）。
- agent 的无害自定义键（如 `myAgentField`）要保留。
- 超过 8 KiB 的 agent meta 被整体丢弃，但 `TrustedMeta` 仍保留。
- 两者皆空时 `Meta == nil`。

---

## B. 支持 `oneOf` / `items.anyOf` 枚举编码

### 现状

`parseElicitationField`（`elicitation.go`）只读 `enum` 与 `items.enum`。
ACP 的 unstable elicitation schema 还允许下面这种写法（`{const, title}` 对）：

```json
{"type":"string","oneOf":[{"const":"safe","title":"Safe mode"},
                          {"const":"fast","title":"Fast mode"}]}
```

数组多选则是 `{"type":"array","items":{"anyOf":[{"const":..,"title":..}]}}`。

现在这类字段解析不出 options，退化成自由文本输入框，用户敲进去的是 title
（"Safe mode"），而 agent 校验的是 const（"safe"），必然失败。

注意：本仓库依赖的 SDK 版本里 `UnstableElicitationSchema.Properties` 是
`map[string]any`，**没有** `StringPropertySchema` / `EnumOption` 这类具名类型，
所以只能按 `map[string]any` 手工解析，不要去 import 不存在的类型。

### 要做的事

`elicitationField` 增加 label→value 的映射能力。建议结构：

```go
type elicitationField struct {
	name        string
	title       string
	description string
	kind        string
	enum        []string   // 展示给用户的 label
	enumValues  []string   // 与 enum 等长；回传给 agent 的值。纯 enum 时两者相同
	multiSelect bool
}
```

解析优先级：`enum` 优先（保持现有行为不变），没有 `enum` 时再看
`oneOf`；数组则 `items.enum` 优先于 `items.anyOf`。

`oneOf`/`anyOf` 的每个元素是 `map[string]any`，取 `const` 作为值、`title`
作为 label；`title` 缺失时 label 回退成 `const` 的字符串形式；`const` 缺失
的元素跳过。`const` 可能是 string/number/bool，复用已有的 `stringSlice`
里那套标量转字符串逻辑（可抽小函数）。

回传时 `elicitationAnswersToContent` 要把用户选的 label 换回 value：
它本来就会用同一份 schema 再调一次 `parseElicitationField`，所以在那里
按 label 查 `enumValues` 即可。查不到（用户自由输入）就原样传，保持现有
的宽容行为。多选同理，逐项转换。

### 测试要求

- `oneOf` 的 `{const,title}` 解析出 options，label 是 title。
- 回答 title 时 content 里是 const。
- `items.anyOf` 多选的解析与回传。
- 已有的纯 `enum` 行为不变（`enum` 与 `oneOf` 同时存在时 `enum` 赢）。
- `const` 为数字/布尔时能正确转字符串再转回。

---

## C. 多选答案的逗号拆分会破坏含逗号的选项

### 现状

前端把多选答案用 `", "` 拼接成一个字符串（`SessionViewer.tsx` 的
`toggleMultiAnswer`，`next.join(", ")`），后端 `splitMultiValue` 无条件
`strings.Split(value, ",")`。于是枚举值本身含逗号时——例如
`["red, green", "blue"]`——用户只选了 `"red, green"` 一项，却被拆成
`["red","green"]` 两个不在枚举里的值，返回给 agent 的 content 不合 schema。

传输格式（`types.AskUserAnswer.Answers` 是 `map[string]string`）是前后端约定，
本次**不改**，只在后端按已知选项做还原。

### 要做的事

新增按已知 label 还原的拆分函数（保留 `splitMultiValue` 作为无选项信息时的
回退，已有测试 `TestSplitMultiValue` 要继续通过）：

```go
func splitMultiValueWithOptions(value string, validLabels []string) []string
```

算法（按顺序短路）：

1. `validLabels` 为空 → 退回 `splitMultiValue`。
2. 整串 trim 后正好等于某个 label → 返回该单项（这一步就修掉了单选含逗号的场景）。
3. 否则按 `,` 切成片段，从左到右做**最长匹配**：尝试把当前片段与后续若干片段
   用 `", "` 重新拼接，取能匹配到 label 的最长组合；匹配成功则消费掉这些片段，
   失败则把当前片段 trim 后单独作为一项（保持对自由输入的宽容）。
4. 丢弃空片段。

调用点两处：`elicitationAnswersToContent`（label 来自 schema 的 enum）与
`buildQuestionAnswers`（label 来自 `types.AskUserQuestionItem.Options`）。
与 B 项叠加时，注意顺序是**先按 label 拆分，再把 label 换成 value**。

### 测试要求

- `["red, green", "blue"]` 中只选 `"red, green"` → 得到一项。
- 同时选 `"red, green"` 和 `"blue"` → 得到两项且都是完整 label。
- 选项不含逗号时行为与旧实现完全一致（防回归）。
- 自由输入的未知值仍按逗号拆分。

---

## D. xAI 扩展响应按问题文本做 key 会丢答案

### 现状

`buildQuestionAnswers` 返回 `map[string][]string`，key 是问题文本。
Grok 若发来两个文本相同、选项不同的问题，`q_0`/`q_1` 两份答案会写进同一个 key，
后者覆盖前者，用户的一个回答静默消失。

这个 map 形状是 xAI runtime 的 serde 要求（`AskUserQuestionExtResponse::Accepted`
的 `answers` 必须是 map，数组会被拒），**不能改成数组**。

### 要做的事

同一 key 的多份答案**合并**而不是覆盖：把后续答案追加进已有切片，并去重
（保持首次出现顺序）。合并时记一条日志说明发生了问题文本重复。

理由：宁可让 agent 收到一个合并后的多值答案，也不要静默丢掉用户的输入。

### 测试要求

- 两个同文本问题、各自有答案 → 合并成一个 key，两个值都在。
- 重复值去重。
- 问题文本互不相同时行为不变（防回归）。

---

## E. 空 schema 的纯确认型 elicitation 被拒

### 现状

`UnstableCreateElicitation` 里：

```go
questions := elicitationSchemaToQuestions(form.Message, form.RequestedSchema)
if len(questions) == 0 {
	return ..., acp.NewInvalidParams(...)
}
```

而 `UnstableElicitationSchema.Properties` 的 SDK 文档明确写着
"Defaults to {} if unset"，即"只有一句话、没有字段"的确认型请求
（"May I proceed?"）是合法的。现在会收到 `-32602`，而我们又在
`Initialize` 里声明了 `elicitation.form` 能力——agent 拿不到 yes/no。

### 要做的事

`len(questions) == 0` 且 `form.Message` 非空时，合成一个确认问题：
question 用 `form.Message`，两个选项（用 `"Yes"` / `"No"` 作为 label），
单选。走同一条 `askUser` 管线。

回答映射：

- 选 Yes → `Accept`，`Content` 为**空 map**（schema 没有字段，不能编造键）。
- 选 No → `Decline` 变体。
- 其余（超时/取消/无会话）→ 沿用现有的 `errElicitationDeclined` 处理路径。

`form.Message` 也为空、又没有任何字段时，保持现在的 `InvalidParams`
（确实无法向用户展示任何信息）。

不要影响 `_x.ai/ask_user_question` 那条路径（它本来就自带 questions）。

### 测试要求

- 空 properties + 非空 message → 不再返回 error，合成出一个确认问题。
- 选 Yes → Accept 且 Content 为空 map（不是 nil 也不是含伪造键的 map，
  按实际实现二选一并在测试里锁死）。
- 选 No → Decline 变体。
- 空 properties + 空 message → 仍是 InvalidParams。

---

## 收尾

全部完成后在仓库根目录留一份 `ACP-ELICITATION-FIXES.md`，逐项写：
改了哪些文件、关键取舍、以及"临时回退验证测试确实会失败"的结果。
不要提交 git，也不要改 `FORK.md`（我来统一登记）。

# ACP elicitation 中低危问题修复记录

方案见 `acp-elicitation-remaining-spec.md`。全部改动集中在
`server/internal/agent/acp/`，未改 `web/` 前端。高危问题（数据竞争、取消不释放、
Kind 缺失、activeSession 污染、多 session 路由）在本次之前已完成，本文只记录
A~E 五项。

## A. 隔离 agent 提供的 `_meta`

**问题**：`convertEvent` 把 agent 的 `_meta` 原样搬进 `types.ToolCall.Meta`，直接
落进 MindFS 的保留键空间——agent 可伪造 `meta.answers`/`questions` 把提问卡片渲染
成已提交、伪造 `meta.source="userShell"` 劫持 shell 合并路径，且大 blob 会被
原样持久化进会话 JSONL 并在回放时重播。

**改法**：
- `SessionUpdate` 增加 `AgentName` 与 `TrustedMeta` 字段。`TrustedMeta` 只在
  MindFS 内部赋值、无 wire 表示，agent 无法伪造；`Raw` 里的 `Meta` 视为不可信。
- 新增 `meta_sanitize.go` 的 `mergeToolCallMeta(agentName, toolCallID, trustedMeta, agentMeta)`：
  ① 逐键丢弃保留键（`reservedMetaKeys`，记日志不含值）；② agent meta 序列化
  超 8 KiB 整体丢弃；③ `TrustedMeta` 覆盖式合并（可信优先）；④ 两者皆空返回 nil。
- `convertEvent` 两处 `Meta:` 改走 `mergeToolCallMeta`。
- `elicitation.go` 里 fork 自己合成的 questions/title/answers 改塞 `TrustedMeta`
  （`askUser` 与 `toolCallCompleteUpdate`），避免被净化剥掉。`Kind` 仍留在 Raw，
  看板清「等待用户」标志依赖它。

**取舍**：不做白名单而是黑名单（`reservedMetaKeys`），因为保留键有语义、数量有限，
其余 agent 自定义键（如 `myAgentField`）应保留透传，方便诊断。被丢弃的键记日志
（不记值），日后发现某 agent 确实需要某键时可补白名单。

**回退验证**：移除接线后 `TestConvertEventPreservesToolCallMeta` /
`TestConvertEventPreservesToolCallUpdateMeta` 失败；移除 8 KiB 限制与 nil 返回后
`TestConvertEventDropsOversizedAgentMetaKeepsTrusted` / `TestConvertEventEmptyMetaIsNil`
失败。

## B. 支持 `oneOf` / `items.anyOf` 枚举编码

**问题**：ACP schema 用 `oneOf: [{const, title}]` 声明枚举时，`parseElicitationField`
只读 `enum`/`items.enum`，退化成了自由文本输入；用户输入 title（"Safe mode"）而
agent 校验 const（"safe"），必然失败。

**改法**：`elicitationField` 增加 `enumValues`（与 `enum` 等长，label→value）。
`parseElicitationField` 解析优先级：`enum` > `oneOf`；数组 `items.enum` >
`items.anyOf`。新增 `parseConstOptions`（读 `{const, title}` 对，title 缺失回退
const，const 缺失跳过）与 `scalarToString`（string/number/bool 统一转字符串，
`stringSlice` 复用）。`elicitationAnswersToContent` 用新增的 `labelToValue` 把
label 换回 value（查不到则原样透传）。

**回退验证**：移除 oneOf/anyOf 解析与 label→value 映射后，7 个 B 测试失败
（`TestParseElicitationFieldOneOf*`、`TestElicitationAnswersToContentOneOf*`、
`TestParseElicitationFieldArrayAnyOf`）。

## C. 多选答案的逗号拆分破坏含逗号的选项

**问题**：前端把多选答案用 `", "` 拼接，后端 `splitMultiValue` 无条件按 `,` 拆分，
`["red, green", "blue"]` 里选 "red, green" 会被拆成两个不在枚举里的值。

**改法**：新增 `splitMultiValueWithOptions(value, validLabels)`：先整串精确匹配单个
label（修掉单选含逗号），再做最长匹配重组（`", "` 前导空格仍随拆分保留，可用
`", "` 重新拼接比对 label），全失败回退朴素拆分。调用点 `elicitationAnswersToContent`
（label 来自 schema enum）与 `buildQuestionAnswers`（label 来自 option）。

**回退验证**：让 `splitMultiValueWithOptions` 直接退回 `splitMultiValue` 后，4 个
C 测试失败（`TestSplitMultiValueWithOptions*`、`TestBuildQuestionAnswersMultiSelectLabelsWithCommas`、
`TestElicitationAnswersToContentMultiSelectLabelsWithCommas`）。

## D. xAI 扩展响应按问题文本做 key 会丢答案

**问题**：`buildQuestionAnswers` 返回 map 的 key 是问题文本；Grok 发两个同文本问题
时 `q_0`/`q_1` 答案互相覆盖，一个回答静默丢失。map 形状是 xAI runtime 的 serde
要求，不能改成数组。

**改法**：同一 key 已存在时合并（追加 + 去重，保持首现顺序），并记日志说明发生
问题文本重复。新增 `mergeStringLists`。

**回退验证**：改回覆盖后 `TestBuildQuestionAnswersMergesDuplicateQuestionText` /
`TestBuildQuestionAnswersMergeDeduplicates` 失败；`TestBuildQuestionAnswersDistinctTextsUnchanged`
锁住不同文本时行为不变。

## E. 空 schema 的纯确认型 elicitation 被拒

**问题**：`UnstableElicitationSchema.Properties` 默认为 `{}`，只有一句话、无字段的
确认请求（"May I proceed?"）合法，但 `UnstableCreateElicitation` 直接返回
`invalid_params`，agent 拿不到 yes/no。

**改法**：`len(questions)==0` 且 `form.Message` 非空时合成单选确认问题
（question=message，Yes/No 两个选项），走同一条 `askUser` 管线。Yes→Accept 且
Content 为空 map（schema 无字段，不编造键）；No→Decline 变体；超时/取消/无会话走
现有 `errElicitationDeclined` 路径。`form.Message` 也为空时保持 `InvalidParams`。
新增 `isAffirmative`。`_x.ai/ask_user_question` 路径不受影响（自带 questions）。

**回退验证**：恢复直接拒绝后 `TestCreateElicitationEmptySchemaWithMessageDeclinesNotInvalid`
与两个 confirm 测试失败；`TestCreateElicitationEmptySchemaAndBlankMessageStillInvalid`
锁住空 message 仍拒绝。

## 其它

- 行尾：fork 新增文件统一 LF；上游文件（`session.go`/`session_test.go`/`process.go`）
  保留 CRLF 不动，Windows checkout 下 `gofmt -l` 报 CRLF 是假阳性，CI 为权威。
- 门槛：`go build ./...`、`go vet ./server/internal/agent/acp/...` 全绿；
  `go test ./server/internal/agent/acp/...` 全绿。`server/internal/session` 的 14 个
  失败是 Windows SQLite TempDir 文件锁的环境性失败（baseline 一致，与本次改动无关）。

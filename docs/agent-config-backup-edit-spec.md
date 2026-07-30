# 配置备份编辑与 Claude 单独 settings 规格

**状态**：已定稿，按此实现  
**范围**：Agent 配置备份的编辑、来源/快照文件编辑、Claude 单独 `settings.json`  
**归属**：fork 功能（实现后登记 `FORK.md`）

---

## 1. 背景与目标

### 1.1 现状问题

- 配置备份创建后只能切换 / 删除，不能编辑清单内容。
- 无法在备份流程中直接编辑配置文件内容。
- 切换备份时，文件按 `sourcePath` 写回用户路径（如 `~/.claude/settings.json`），会污染本机 Claude Code。
- Claude Code 官方行为：`settings.json` 的 `env` 会覆盖进程环境变量；MindFS 对 claude 的 API 切换主要走 `agents.json` Env，但若恢复用户 settings，仍可能互相影响。

### 1.2 目标

1. **编辑已有备份**：可改名称以外的可编辑字段（见下）、fileSources、env、Claude 单独 settings 选项。
2. **文件编辑**：
   - **新建备份**：从来源路径加载内容到编辑缓存；保存写入**备份快照路径**，**不改**来源原文件。
   - **编辑已有备份**：打开**备份快照路径**上的文件；保存写回快照。
3. **Claude 单独 settings**（仅 claude / claude-sdk）：
   - 备份级开关 + 独立路径 P。
   - 切换该备份时：**不**覆盖用户默认 settings；将快照恢复到 P；启动 Claude SDK 时 `WithSettingsPath(P)`。

### 1.3 非目标（本迭代不做）

- 整包 `CLAUDE_CONFIG_DIR` 深隔离（会话 / skills 全搬家）。
- 切换前自动备份当前配置（可后续 P0）。
- 编辑「磁盘活文件」作为默认路径（本规格明确默认编快照）。
- 非 Claude Agent 的独立 config dir（codex `CODEX_HOME` 等）本迭代不改。
- Provider（API 供应商）流程不变。

---

## 2. 术语

| 术语 | 含义 |
|---|---|
| **来源路径 (sourcePath)** | 创建备份时从哪读、或清单记录的「逻辑来源」路径 |
| **备份路径 / 快照路径 (backupPath)** | `agents-config/<backupId>/00N-filename` 下的快照文件 |
| **活文件** | 磁盘上来源路径指向的当前文件 |
| **独立 settings 路径 P** | MindFS 管理的 Claude settings 文件路径，非用户默认 `~/.claude/settings.json` |
| **切换** | `POST /api/agent-config/switch`：恢复 env/文件并记录 last selection、杀进程、探活 |

---

## 3. 数据模型

### 3.1 Manifest 条目扩展

路径：`<MindFSConfigDir>/agents-config/manifest.json`

现有字段保留，新增：

```json
{
  "id": "claude-work",
  "agent": "claude",
  "name": "work",
  "createdAt": "...",
  "updatedAt": "...",
  "sources": [
    {
      "sourcePath": "C:\\Users\\...\\.claude\\settings.json",
      "backupPath": "claude-work/001-settings.json"
    }
  ],
  "envKeys": ["ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY"],
  "isolatedClaudeSettings": true,
  "claudeSettingsPath": "C:\\Users\\...\\AppData\\Roaming\\mindfs\\claude-settings\\claude-work.json"
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `isolatedClaudeSettings` | bool | 默认 `false`。为 true 时，切换不把 Claude settings 类快照写回用户默认路径 |
| `claudeSettingsPath` | string | 独立路径 P。`isolatedClaudeSettings=true` 时必填（服务端可自动生成） |

**约束：**

- `isolatedClaudeSettings` 仅当 `agent` 规范化后为 `claude`（含 claudecode / claude-code 别名）时有效；其它 agent 忽略或创建时拒绝。
- P 必须落在 MindFS 配置目录下的受控子目录（见 3.3），禁止任意路径写入（防路径穿越）。
- `id` 仍为 `agent + "-" + name`；**本迭代名称创建后不可改**（避免 id 迁移）。编辑已有备份时名称只读。

### 3.2 环境变量快照

仍为：`<MindFSConfigDir>/agents-env.json`  
`id → ["KEY=value", ...]`  
编辑备份时更新该 id 的条目。

### 3.3 独立 settings 目录

```text
<MindFSConfigDir>/claude-settings/<backupId>.json
```

- 默认 P = 上述路径。
- 用户可在 UI 覆盖，但服务端校验：解析后的绝对路径必须在 `claude-settings/` 目录内（`filepath.Rel` 不得逃逸）。

### 3.4 快照内容与 Claude settings 的关系

当 `isolatedClaudeSettings=true`：

- 创建/更新时：若 fileSources 中含 settings 类文件，或单独指定了 Claude settings 源，内容写入：
  1. 常规 `sources[].backupPath` 快照（用于编辑/历史），且
  2. 同步/切换时以 P 为**运行时落盘目标**。
- 建议：Claude 的 settings 在 sources 中有一条记录；切换时该条 **restore target = P**，而不是 `sourcePath`。

实现约定（推荐）：

```text
sourcePath  = 创建时读取来源（可记录用户路径，仅作 provenance）
backupPath  = 快照相对路径
runtimePath = 可选；若 isolatedClaudeSettings 且该文件是 claude settings，则 runtimePath = P
```

为减少 schema 膨胀，本迭代可用：

- `isolatedClaudeSettings` + `claudeSettingsPath` 全局于 entry；
- 切换时：凡 `filepath.Base(sourcePath/backupPath)` 匹配 `settings.json`（或标记为 claude settings 的那一条）→ 写到 P；其它 sources 仍写 `sourcePath`（若 claude 备份通常只有 env + settings，则只有 settings 走 P）。

**更干净的实现（本规格采用）：**

- Claude 单独 settings **不占用** 普通 fileSources 的用户路径恢复。
- 内容存在：`agents-config/<id>/claude-settings.json`（固定 backup 相对名）+ 运行时复制到 P。
- 普通 `file_sources` 仍按原逻辑；Claude settings 由 `isolatedClaudeSettings` 专用通道管理。

---

## 4. API

前缀均需现有 E2EE/保护中间件（与现有 agent-config 一致）。

### 4.1 现有接口变更

#### `POST /api/agent-config/backups`（创建 / 覆盖）

Request 扩展：

```json
{
  "agent": "claude",
  "name": "work",
  "file_sources": ["..."],
  "env_lines": ["KEY=value"],
  "overwrite": false,
  "isolated_claude_settings": true,
  "claude_settings_path": "",
  "file_contents": [
    { "source_path": "...", "content": "..." }
  ],
  "claude_settings_content": "{...}"
}
```

| 字段 | 说明 |
|---|---|
| `file_contents` | 可选。若提供，对应 `source_path` **以 content 写入快照**，不再从磁盘读该路径（新建时「编辑后保存到备份」） |
| `isolated_claude_settings` | 仅 claude |
| `claude_settings_path` | 空则服务端生成默认 P |
| `claude_settings_content` | 可选。有则写入 claude settings 快照；无且 isolated 时，可从来源默认 settings 路径读一次 |

校验：

- 与现有相同：name 模式、agent 存在、sources 或 env 或 claude_settings 至少一种内容。
- isolated 为 true 且 agent 非 claude → 400。
- `file_contents` 的 source_path 必须出现在 `file_sources` 中（或允许仅 content 条目，服务端补进 sources）。

Response：扩展后的 manifest entry（含新字段）。

#### `POST /api/agent-config/switch`

行为扩展：

1. 加载 entry。
2. 若 `isolatedClaudeSettings`：
   - 将 claude settings 快照写到 `claudeSettingsPath`（P）；
   - **不要**把该 settings 快照 copy 到用户 `~/.claude/settings.json`；
   - 普通 sources（若有）仍按 sourcePath 恢复（需二次确认逻辑：仅对会写用户路径的目标做 exists 检查）。
3. Env 恢复逻辑不变。
4. Preferences：`last_config_selection` 仍记 backup id/name；**额外**持久化 claude 运行时 settings 路径（见 4.4）。
5. KillAgent + probe 不变。

`needs_confirm`：对将要覆盖的**已存在目标文件**确认；P 若已存在也确认（或静默覆盖 P，因 P 属 MindFS 托管——**规格采用：P 静默覆盖，用户路径仍需确认**）。

### 4.2 新增：更新备份清单

#### `PUT /api/agent-config/backups`

```json
{
  "id": "claude-work",
  "file_sources": ["..."],
  "env_lines": ["..."],
  "isolated_claude_settings": true,
  "claude_settings_path": "..."
}
```

- **不可改** `agent`、`name`、`id`。
- 更新 sources：  
  - 新路径：从磁盘拷入新快照（或接受后续 file PUT）；  
  - 已有路径：保留原 backupPath 文件除非客户端上传新 content；  
  - 删除的路径：删除对应快照文件。
- 更新 env 快照与 `envKeys`。
- 更新 isolated 字段；若打开 isolated 且无 claude settings 快照，可要求客户端先 PUT content 或从默认源导入。

Response：更新后的 entry。

### 4.3 新增：读写快照文件内容

#### `GET /api/agent-config/backups/file`

Query：

- `id`：backup id（必填）
- `backup_path`：manifest 中的相对 backupPath（普通文件）  
  **或**
- `kind=claude_settings`：读 claude settings 快照

Response：

```json
{
  "id": "claude-work",
  "backup_path": "claude-work/001-settings.json",
  "content": "...",
  "size": 1234
}
```

限制：

- `backup_path` 必须属于该 id，且 `Clean` 后不逃逸 `agents-config/<id>/`。
- 最大读取大小：例如 1 MiB（可配置常量）；超限 413。
- 非 UTF-8 文本：仍按字节读出，前端以文本方式编辑；可选 `encoding` 探测失败则 415。

#### `PUT /api/agent-config/backups/file`

```json
{
  "id": "claude-work",
  "backup_path": "claude-work/001-settings.json",
  "content": "...",
  "kind": ""
}
```

或 `"kind": "claude_settings"` 写 claude settings 快照（并可选同步到 P 若当前 last selection 就是该 backup——本迭代 **仅写快照**，同步到 P 发生在 switch）。

- 写盘：tmp + rename。
- 权限 0600。
- 更新 manifest `updatedAt`。

### 4.4 新增：新建时预览来源文件（不写备份）

#### `POST /api/agent-config/preview-file`

```json
{ "path": "~/..." }
```

Response：`{ "path": "<abs>", "content": "..." }`

- 仅允许普通文件、大小限制同 4.3。
- 用于新建流程：加载来源 → 编辑缓存 → 创建时用 `file_contents` 提交。

### 4.5 Preferences 扩展（Claude 运行时 settings）

在现有 `LastConfigSelection` 旁或同结构增加（实现任选其一，需文档化）：

**方案 A（推荐）**：`preferences` 中 per-agent：

```json
"agent_runtime": {
  "claude": {
    "settings_path": "<P or empty>"
  }
}
```

- switch 且 isolated → 写入 P。
- switch 非 isolated 或非 claude → 清空 settings_path。
- `OpenSession` 时读取：非空则 `WithSettingsPath`。

**方案 B**：仅依赖 last backup id，OpenSession 时查 manifest——耦合更重，重启后仍可用。  
本规格采用 **A + 切换时写入**；OpenSession 以 preferences 为准，backup 删除时可清 runtime。

---

## 5. Claude 启动行为

`server/internal/agent/claude/session.go` / Pool 打开 session 时：

```text
env := def.Env
options := [WithCwd, WithEnv(env), ...]
if settingsPath := runtime Claude settings_path; settingsPath != "" {
  options += WithSettingsPath(settingsPath)
  // 建议同时：不加载 user SettingSources，避免与用户 ~/.claude/settings.json 合并
  // 若 SDK 接线允许：WithSettingSources(空) 或仅 project
}
```

**SettingSources 本迭代最低要求：**

- 有 `WithSettingsPath(P)` 即视为完成主目标（不覆盖用户文件 + SDK 读 P）。
- **宜**：避免再加载 user 层 settings（若 Go SDK 当前仅 SkillsConfig 传 `--setting-sources`，实现时实测；能关则关 user）。
- **文档声明**：若仍合并 user settings，则「不写用户盘」已保证，「运行时完全不读用户 settings」可能不完整——实现 PR 中写明实测结果。

**settings.env vs 进程 Env：**

- 保持 Claude 官方优先级（settings.env 可覆盖进程）。
- 产品文案建议：API Key / Base URL 放备份 Env；单独 settings 尽量不写同名 env。  
- 本迭代不做自动 strip（可列 follow-up）。

---

## 6. 前端交互

### 6.1 新建备份（flow=backup, 创建）

在现有 `AgentConfigPopover` 备份表单上扩展（可仍内嵌 FileTree，或拆子组件）：

1. 备份名称（可编辑）。
2. 配置来源：每行路径 + **[编辑]**。
   - 点编辑 → 调 preview-file 加载内容 → 模态/面板文本编辑。
   - 保存编辑 → **仅更新前端缓存** `Map<sourcePath, content>`，不写盘。
3. 环境变量：保持多行编辑。
4. **若 agent 为 claude**：
   - 开关「使用单独 settings.json」（默认 **开** 推荐，或默认关——**规格采用默认开**，降低误覆盖风险）。
   - 路径输入：默认占位「自动生成」；可改（须通过服务端校验）。
   - **[编辑]** settings 内容：可先 preview 用户默认 settings 或空 JSON；缓存 `claude_settings_content`。
5. 点「保存」→ `POST /backups`，带上 `file_contents`（仅编辑过的）、isolated 字段、claude_settings_content。

未点过来源编辑的路径：服务端仍从磁盘 copy（兼容现行为）。

### 6.2 编辑已有备份

入口：切换列表每条备份旁增加 **[编辑]**（或选中后「编辑」按钮）。

打开编辑态（可用同一 popover 的 `step=edit`）：

1. 名称：**只读**。
2. 配置来源列表：展示 `sources`（sourcePath 展示，backupPath 用于编辑）。
   - **[编辑]** → `GET .../file?id&backup_path` → 编辑 → `PUT .../file`（直接改快照）。
3. Env：加载 agents-env 对应行（需 API：list 已有 entry 时不下发 secret env 值——**现有 list 只有 envKeys**）。  
   - **问题**：编辑 env 需要当前值。  
   - **规格**：新增 `GET /api/agent-config/backups/env?id=` 返回 env_lines（本机自托管，与备份明文存储一致）；或 PUT 时全量提交。前端编辑时先 GET env。
4. Claude 单独 settings 开关与路径；编辑 content → kind=claude_settings。
5. 保存清单变更 → `PUT /backups`。

### 6.3 切换

- 逻辑仍选 backup → switch。
- 若 entry.isolatedClaudeSettings：成功后 toast/文案可提示「已使用单独 settings，未修改用户 ~/.claude/settings.json」。
- 确认框：仅当会覆盖用户路径文件时出现。

### 6.4 文件编辑器 UI

- 简单全宽 textarea 或现有编辑器组件；等宽字体。
- 标题显示路径（新建显示来源路径 + 注明「保存到备份」；编辑显示快照路径）。
- 保存 / 取消；busy 态；错误展示。
- 大小超限提示。

### 6.5 i18n

`zh-CN` / `en-US` 增加键，例如：

- `agentConfig.editBackup`
- `agentConfig.editSourceFile`
- `agentConfig.editSnapshotFile`
- `agentConfig.isolatedClaudeSettings`
- `agentConfig.isolatedClaudeSettingsHelp`
- `agentConfig.claudeSettingsPath`
- `agentConfig.saveToBackupNotSource`
- …

---

## 7. 安全

| 项 | 要求 |
|---|---|
| 路径 | 所有 backup_path / claude_settings_path 规范化后限制在配置根下 |
| 来源预览 | 仅文件、非目录；拒绝非常规设备文件 |
| 大小 | 读写上限（建议 1 MiB） |
| 权限 | 快照与 P 文件 0600 |
| 穿越 | `..`、绝对路径拼进 backup 树均拒绝 |
| 鉴权 | 与现有 protectedEndpoint 一致 |

---

## 8. 实现分期

### Phase 1 — 后端骨架 + 规格落地（本 PR 优先）

1. Manifest 字段扩展与读写兼容（旧 manifest 无新字段视为 false）。
2. `POST /backups` 支持 isolated + file_contents + claude_settings_content。
3. `PUT /backups` 更新清单与 env。
4. `GET/PUT .../file`、`GET .../env`、`POST preview-file`。
5. `switch` 支持 isolated → 写 P、不写用户 settings；preferences 记 settings_path。
6. Claude `OpenSession` 读 preferences → `WithSettingsPath`。
7. 单测：路径校验、isolated switch、file 读写边界。

### Phase 2 — 前端

1. 新建：来源行 [编辑]、缓存 file_contents、claude 开关。
2. 已有：编辑入口、快照编辑、env 加载编辑、PUT 保存。
3. i18n、错误与确认文案。

### Phase 3 — 打磨（可另 PR）

1. SettingSources 实测与关 user。
2. 切换前自动备份。
3. settings.env 与进程 Env 冲突提示 / strip。
4. FORK.md 登记。

---

## 9. 测试用例（验收）

| # | 场景 | 期望 |
|---|---|---|
| 1 | 新建备份，编辑来源内容后保存 | 快照含新内容；来源磁盘文件不变 |
| 2 | 编辑已有备份快照并保存 | 仅 backupPath 文件变；sourcePath 磁盘不变 |
| 3 | Claude + isolated 创建并切换 | P 存在且内容正确；`~/.claude/settings.json` 未变 |
| 4 | 切换后开 Claude session | 进程带 SettingsPath=P（日志或集成可测） |
| 5 | 非 claude 传 isolated | 400 |
| 6 | backup_path 含 `..` | 400 |
| 7 | 超大文件 preview | 413 |
| 8 | 旧 manifest 无新字段 switch | 行为与现在一致 |

---

## 10. FORK 登记（实现合并时）

| 范围 | 内容 | 归属 |
|---|---|---|
| `server/internal/api/agent_config.go` 等 | 备份编辑 API、isolated Claude settings、switch 写 P | 我方 |
| `server/internal/agent/claude/session.go` | `WithSettingsPath` | 我方 |
| `server/internal/preferences` | agent runtime settings_path | 我方 |
| `web/...` FileTree / agentConfig | 编辑 UI | 我方 |
| `docs/agent-config-backup-edit-spec.md` | 本规格 | 我方 |

---

## 11. 决策摘要（已定）

| 议题 | 结论 |
|---|---|
| 单独 settings 放哪 | **绑在备份上**；切换默认沿用 |
| 切换时是否每次询问 | **否**（高级覆盖非本迭代） |
| Claude 默认是否 isolated | **默认开** |
| 新建点编辑编谁 | 来源加载到缓存 → **保存进快照**，不改源文件 |
| 已有备份点编辑编谁 | **快照路径** |
| 备份改名 | 本迭代 **不支持** |
| ConfigDir 深隔离 | **不做**（本迭代） |

---

## 12. 修订记录

| 日期 | 说明 |
|---|---|
| 2026-07-29 | 初稿定稿，对应会话中产品讨论结论 |

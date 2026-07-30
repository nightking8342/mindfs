# Fork 维护说明

本仓库是 [a9gent/mindfs](https://github.com/a9gent/mindfs) 的长期维护 fork：

- `origin`   → https://github.com/nightking8342/mindfs （本 fork，主干 `main`）
- `upstream` → https://github.com/a9gent/mindfs （上游）

维护原则：**控制 diff 面积**。能加新文件就不改上游文件，能走配置（`agents.json`、环境变量）就不动核心代码，让每次上游合并的冲突尽量少。

## 与上游的分歧清单

每一处与上游的故意分歧都记录在这里。合并上游遇到冲突时，按「归属」列决定保留哪边；新增分歧时必须同步更新本清单。

| 文件 / 范围 | 分歧内容 | 冲突归属 |
|---|---|---|
| `FORK.md`、`.github/workflows/ci.yml` | fork 独有文件，上游没有 | 我方 |
| `CLAUDE.md` 的「Fork 说明」小节 | fork 独有小节；若上游日后自己加了 CLAUDE.md，合并其内容但保留本小节 | 手动合并 |
| `web/package.json` 的 `allowScripts` 字段 | 允许 esbuild 安装脚本（本地构建需要） | 我方（版本号等其余字段随上游） |
| Android WebView 文字缩放设置与 IME 选区修复（e587493） | fork 自研功能，上游未合入 | 我方 |
| `web/src/App.tsx` 的 `TOKEN_STATION_ENABLED` 开关 | 置 `false` 隐藏官方 Token 加油站入口并停掉其后台轮询；恢复或指向自建中转站时置 `true` | 我方（开关及三处引用；面板 JSX 本体随上游） |
| 配置备份编辑 + Claude 单独 settings（`docs/agent-config-backup-edit-spec.md`） | 备份清单可更新；快照文件读写；Claude `isolatedClaudeSettings` 切换写独立路径并用 `WithSettingsPath`，不覆盖用户 `~/.claude/settings.json` | 我方（`server/internal/api/agent_config.go` + `http.go` 路由、`preferences/store.go`、`agent/claude/session.go` + `pool.go` + `types/types.go`、`api/usecase/session.go`、`web/src/services/agentConfig.ts`、`web/src/components/FileTree.tsx` 的 `AgentConfigPopover`、两份 i18n locale） |
| 配置切换进度展示（`docs/agent-config-switch-progress-spec.md`） | `switchAgentConfig` 返回步骤清单（失败时也带）；探活结束无条件广播 `agent.config.switched`，因为 `agent.status.changed` 会被 `Prober.statusChanged` 过滤掉 | 我方（`agent_config.go`、`appcontext.go`、`helpers.go` 的 `respondErrorWithExtra`、`agent_api_provider.go` 调用处、`App.tsx`、`FileTree.tsx`、`services/agentConfig.ts`、`services/error.ts` 的 `agent.config_switched`） |
| Agent 配置弹层点击外部关闭 | 上游只在「选择 Agent」一步监听外部点击；fork 扩展到全部步骤（全屏文件编辑器 `file` 步骤除外，避免丢草稿） | 我方（`web/src/components/FileTree.tsx` 的 click-outside effect） |
| `web/tests/agent-lifecycle-restart.test.mjs` 的英文文案断言 | 上游该断言与其自身 `en-US.ts` 实际文案不一致（v0.4.4 tag 内自相矛盾，upstream/main 亦未修）；fork 把断言对齐实际文案 `Agent config switch & restart` | 我方（上游日后若统一文案与断言，随上游并删除本行） |
| `release-notes.md` | 跟随上游发版记录，fork 不自行发版 | 上游 |
| `go.mod` / `go.sum` 的 `replace` 指令 | 指向 `github.com/yandc/*`，由上游维护 | 上游 |

### 暂未分歧、但日后改动时须登记的敏感点

以下是上游与 a9gent.com 商业服务的挂钩点。目前 fork **尚未改动**它们；一旦改动（如指向自建服务），必须在上表登记：

- `agents.json` 的 `relayBaseURL` / `tokenStationURL`（官方中继与 Token 中转站入口；运行时可用 `MINDFS_RELAY_BASE_URL` 环境变量覆盖，优先走这条路而不是改代码）
- `server/internal/update/service.go` 与 `server/app/server.go` 中硬编码的 `a9gent/mindfs` 仓库名、`relay.a9gent.com/mindfs-downloads` 下载源（自更新通道）
- `server/internal/relay/tips.go`（官方推广卡片轮询，`-norelayer` 或清空 relayBaseURL 即不请求）
- `scripts/install.sh` / `scripts/install.ps1` 中的仓库名与下载地址

## 上游同步流程

跟着上游的 **release tag** 合并（而不是追每个 commit）。步骤：

1. 工作区必须干净（`git status` 无未提交改动）。
2. `git fetch upstream --tags`
3. `git merge <tag>`（如 `git merge v0.4.4`；merge 而非 rebase，保留独立 merge commit，不 squash）
4. 解冲突：按上表「冲突归属」处理；表里没有的冲突按常规判断并考虑是否登记。
5. 门槛检查：`go test ./...` 和 `cd web && npm run typecheck` 全部通过。
   注意：Windows 本地跑 `go test ./...` 存在已知的环境性失败（POSIX 文件权限断言、SQLite 临时目录文件锁、部分测试会读到真实用户目录的 skills / 项目注册表），**以 CI 的 Linux 结果为权威门槛**，本地失败时先对照 CI 判断是否为环境问题。
6. 回归扫描：`git grep a9gent.com -- ':!*.md'` 对比合并前后，确认上游新增代码没有引入绕过配置的硬编码官方地址；有则评估处理并登记。
7. 全部通过后 push，等 CI 变绿。

## 提交规范

- fork 自己的改动：提交信息加 `fork:` 前缀（如 `fork: 支持自建中继地址`），方便 `git log` 区分来源。
- 上游合并：保留默认 merge commit 信息（`Merge tag 'v0.4.4' ...`）。

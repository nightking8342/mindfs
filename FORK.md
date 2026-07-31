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
| fork UI 隔离层（`ForkShell` + `fork-theme.css`） | fork 要改 UI 布局与视觉，但上游 `App.tsx` 有 1.5 万行、布局装配在文件尾部，直接改必然每次合并都在巨型文件里解冲突。改为接管布局壳：新增 `web/src/layout/ForkShell.tsx`（实现与上游 `AppShell` 相同的五槽位接口，尺寸走 `--fork-*` CSS 变量，各区域挂 `data-fork-region` / `data-fork-viewport` 钩子）+ `web/src/styles/fork-theme.css`（视觉覆盖层，加载于 `index.css` 之后）。**上游 `layout/AppShell.tsx` 与 `index.css` 一律不改**，对上游文件的 diff 仅两行 import。`ForkShell` 内含 props 契约断言（`Equals<ForkShellProps, UpstreamShellProps>`），上游增删改 `AppShell` 的 prop 时 `npm run typecheck` 直接失败，据此同步；`?forkShell=0` 可临时回退上游 `AppShell` 用于合并后对照排查 | 我方（`web/src/layout/ForkShell.tsx`、`web/src/styles/fork-theme.css` 为 fork 独有文件；`App.tsx` 的 `import { ForkShell as AppShell }` 一行与 `main.tsx` 的 `import "./styles/fork-theme.css"` 一行。上游改 `AppShell.tsx` 内部实现时**取上游版本**（该文件仍归上游），但 `ForkShell` 不会自动继承这些改动，需按同步流程第 7 步逐条评估是否 port——尤其是移动端踩坑补丁；props 形状变化则由契约断言在 `typecheck` 阶段强制暴露） |
| 配置备份编辑 + Claude 单独 settings（`docs/agent-config-backup-edit-spec.md`） | 备份清单可更新；快照文件读写；Claude `isolatedClaudeSettings` 切换写独立路径并用 `WithSettingsPath`，不覆盖用户 `~/.claude/settings.json` | 我方（`server/internal/api/agent_config.go` + `http.go` 路由、`preferences/store.go`、`agent/claude/session.go` + `pool.go` + `types/types.go`、`api/usecase/session.go`、`web/src/services/agentConfig.ts`、`web/src/components/FileTree.tsx` 的 `AgentConfigPopover`、两份 i18n locale） |
| 配置切换进度展示（`docs/agent-config-switch-progress-spec.md`） | `switchAgentConfig` 返回步骤清单（失败时也带）；探活结束无条件广播 `agent.config.switched`，因为 `agent.status.changed` 会被 `Prober.statusChanged` 过滤掉 | 我方（`agent_config.go`、`appcontext.go`、`helpers.go` 的 `respondErrorWithExtra`、`agent_api_provider.go` 调用处、`App.tsx`、`FileTree.tsx`、`services/agentConfig.ts`、`services/error.ts` 的 `agent.config_switched`） |
| Agent 配置弹层点击外部关闭 | 上游只在「选择 Agent」一步监听外部点击；fork 扩展到全部步骤（全屏文件编辑器 `file` 步骤除外，避免丢草稿） | 我方（`web/src/components/FileTree.tsx` 的 click-outside effect） |
| 备份清单持久化的抗截断处理 | 上游 `manifest.json` / `agents-env.json` 用 `os.WriteFile` 非原子写（先截断再写），且读到 0 字节文件时静默当作「无备份」；两者叠加使一次中断的写入被后续「创建备份」固化——清单被覆盖成仅剩新建的一条，而快照目录仍在磁盘上。fork 改为 `writeFileAtomic` 写入，并把 0 字节文件视为损坏直接报错（正常写入最小产物是 `[]` / `{}`，不会为空） | 我方（`server/internal/api/agent_config.go` 的 `read/writeAgentConfigManifest`、`read/writeAgentEnvBackups`；新增 `agent_config_manifest_test.go`。上游日后自行修同类问题可评估回退） |
| `agent_config_test.go` 的 Windows 配置目录隔离 | 上游只设 `HOME` / `XDG_CONFIG_HOME`，而 Windows 的 `os.UserConfigDir()` 读 `%AppData%`，导致该测试在 Windows 上直接读写**真实用户**的备份清单并将其覆盖；fork 补设 `AppData` / `USERPROFILE`（同文件内 `setupAgentConfigTest` 早已这么做） | 我方（上游日后补齐隔离则随上游并删除本行） |
| `switchAgentConfig` 的 `apply_env` 步骤状态 | fork 规格 `agent-config-switch-progress-spec.md` §4.3 要求「备份无 env 时跳过 `apply_env`」，但实现一律记 `ok`，与 fork 自带测试 `TestSwitchSkipsEnvStepWhenBackupHasNoEnv` 矛盾；改为无 `envKeys` 时记 `skipped`，同时保留必须执行的旧 env 清除动作 | 我方（属「配置切换进度展示」功能范围） |
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
7. 布局层扫描：`git log <上次合并的 tag>..<本次 tag> --oneline -- web/src/layout/AppShell.tsx`。fork 的 `ForkShell` 接管了布局，**不会自动继承上游对 `AppShell` 的改动**，因此有输出就要逐条评估是否 port。
   - 优先看移动端修复（`fix.*mobile` / `ios` / safe-area / IME / viewport height 这类）：`ForkShell` 里的 `--mindfs-safe-area-top`、`--mindfs-viewport-height`、`willChange`、`backfaceVisibility`、`transform: translateZ(0)` 全是上游踩坑换来的补丁，漏掉在桌面浏览器上测不出来，只会在真机上炸。
   - 主题/配色类改动通常已被 `--fork-*` 变量吸收，确认一遍即可。
   - 需要对照上游布局效果时，页面 URL 加 `?forkShell=0` 可临时切回上游 `AppShell` 渲染。
   - props 形状变化不用靠人眼：`ForkShell` 内的契约断言会让 `npm run typecheck` 直接失败。
8. 全部通过后 push，等 CI 变绿。

## 提交规范

- fork 自己的改动：提交信息加 `fork:` 前缀（如 `fork: 支持自建中继地址`），方便 `git log` 区分来源。
- 上游合并：保留默认 merge commit 信息（`Merge tag 'v0.4.4' ...`）。

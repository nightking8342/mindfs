# AGENTS.md

This file provides guidance to coding agents when working with code in this repository.

## Fork 说明

本仓库是 `a9gent/mindfs` 的长期维护 fork（`origin` = nightking8342/mindfs，`upstream` = a9gent/mindfs）。与上游的全部分歧、冲突归属规则、上游同步流程和提交规范见：

@FORK.md

改动上游文件前先查 FORK.md 的敏感点清单；产生新分歧时必须同步登记。

## 项目定位

MindFS 是一款「AI Agent 远程访问网关 + 结果可视化」桌面/移动应用：Go 后端（`cli/` + `server/`）承载 HTTP/WebSocket API，React 19 + Vite + Tailwind 前端（`web/`）作为唯一 UI，Capacitor 壳打包 Android/Harmony 客户端。发布形态是单个静态编译的 Go 二进制 + 同目录的 `web/` 静态资源（**不使用 `embed`**）。

## 常用命令

```bash
# 开发
make dev                       # 前后端一起跑（Go 直接 serve web/dist），默认 :7331；ADDR=:9000 可覆盖
make dev-backend               # 仅后端（走 server/cmd/mindfs-server 入口）
make dev-web                   # 仅 Vite dev server（前端改动即时热更）

# 构建
make build-web                 # 产出 web/dist
make build                     # build-web + go build -> ./mindfs
make build-all                 # 交叉编译 darwin/linux/windows amd64+arm64+armv7 到 dist/
make build-android             # web build:android + gradle assembleRelease -> dist/*.apk
make build-harmony             # web build:harmony + hvigor assembleHap -> dist/*.hap
make install PREFIX=~/.local   # 装二进制 + agents.json + task_template.json + web 到 <prefix>

# 测试
make test                      # 等价于 go test ./...
go test ./server/internal/api/... -run TestXxx -v   # 跑单个测试

# 前端
cd web && npm run typecheck    # tsc --noEmit（**没有 lint/format 脚本**，类型检查即门槛）
cd web && npm run build        # 与 make build-web 等价

# 发布（需要 gh CLI + MINDFS_RELEASE_PUBLIC_KEY/PRIVATE_KEY 环境变量）
make release TAG=v1.2.3                       # 前置：release-notes.md 首行 `# MindFS v1.2.3` 必须匹配 TAG
make release TAG=v1.2.3 RELEASE_ANDROID=1     # 附带 APK
make verify-release TAG=v1.2.3                # 验证已签名的 manifest 与产物
```

Windows 环境额外要点：`make` 目标里大量使用 `install(1)`、`bash scripts/*.sh`，需要 Git Bash / MSYS 环境；纯 PowerShell 下应直接调用底层的 `go build ./cli/cmd` 和 `cd web && npm run build`。

### 改动验证 / 部署的职责区分

- **前端改动（`web/`）**：走常驻的 Vite dev server 验证，改完只需 `cd web && npm run typecheck`。**不要**为前端改动触发部署——dev server 即时热更，部署反而打断流程。
- **Go 后端改动（`server/`）**：先 `go test ./server/internal/... -run TestXxx -v` 跑相关单测（注意 Windows 本地存在已知环境性失败，以 CI 为权威门槛，见上文），**真机验证再走部署**。后端改动才需要构建二进制并触发部署（本机部署规范见 @docs/windows-redeploy.md；只改 Go 时不必重跑 `web/build`）。
- 跨层排查（后端到前端通知等）可同时用 dev server 看前端 + 部署跑后端，但改动落地推送到仓库或部署要以对应层各自的验证门禁为准。

## 部署到本机（Windows）

用户常通过 MindFS 自身托管的会话来指挥 Agent，此时进程树是 `mindfs.exe → claude.exe / codex.exe → bash.exe`。停服务走 `taskkill /T`（树杀），**在会话里直接停 mindfs 会把 Agent 自己一起杀死**，部署卡在半路、服务起不来。

因此部署必须交给脱离进程树的计划任务，不要自己发明方式：

```bash
npm --prefix web run build                                # 脚本不负责构建，
PATH="/c/Go/bin:$PATH" go build -o mindfs.exe ./cli/cmd    # 忘了构建会静默部署旧产物
schtasks /Run /TN MindFSRedeploy                          # 带 45s 延迟，够 Agent 把话说完
```

权限模型（四个任务、普通/管理员的委派）、失败排查对照表、日志位置见：

@docs/windows-redeploy.md

## 高层架构

### 后端分层

```
cli/cmd/mindfs.go        —— 用户直接调用的 CLI 入口。负责：解析 flag / config.json / 环境
                             变量、启停后台 daemon（Unix 走双 fork，Windows 走 detached process）、
                             浏览器唤起、-task 子命令、-update 自更新。真正跑服务的是子进程；
                             父进程通常 exec 完就退出。

server/cmd/mindfs-server —— 纯粹的 server 进程入口（不带 daemon 逻辑），make dev-backend 走的
                             也是它，方便调试。

server/app/server.go     —— `Start(ctx, addr, opts)` 是所有装配的中心：加载 Registry（受管项目
                             列表）→ Agent 配置 + Pool + Prober → Preferences → e2ee →
                             Notify / WebPush → Relay 服务 → Kanban / Scheduled → 挂 HTTP/WS
                             handler。**静态资源目录不是 embed 的**，`resolveStaticDir()` 依次
                             尝试 `MINDFS_STATIC_DIR` 环境变量 → `<exe>/web/dist`（源码布局）→
                             `<exe>/web`（发布包解压布局）→ `<prefix>/share/mindfs/web`（安装
                             布局）。改前端后走 `make build-web` 才会被后端看到。

server/internal/api      —— HTTP + WS 分发层。`http.go` 挂 REST、`ws.go` 挂 JSON-RPC over
                             WebSocket；单一职责的接口再拆到 `http_scheduled.go` /
                             `http_tasks.go` / `http_token_station.go` / `http_webpush.go` /
                             `http_relay_services.go`。所有跨请求的业务逻辑都藏在
                             `api/usecase/` 下的 Service（sessions / fs / prompts /
                             external_sessions / git_worktree / candidates / local_dirs 等）。
                             `AppContext` 是 handler 共享的一堆依赖注入。

server/internal/agent    —— 多 Agent 抽象层。协议由 `protocol.go` 三选一：
                               • ProtocolClaudeSDK — claude-agent-sdk-go
                               • ProtocolCodexSDK  — codex-go-sdk（app-server 通道）
                               • ProtocolACP       — 通用 ACP JSON-RPC / ndJSON
                             `pool.go` 是 session 路由中心；`acp/`、`claude/`、`codex/` 三个子
                             包各自实现 Runtime。`discovery.go` + `probe.go` 定期扫描本机装了
                             哪些 Agent CLI，`importers.go` + 各协议子包里的 importer 负责把
                             CLI 自身的历史会话映射成 MindFS 内部 session。

server/internal/session  —— 会话生命周期管理，与 agent Pool 解耦。

server/internal/kanban   —— 任务看板：`template_store.go` 管任务模板（对应 task_template.json），
                             `task_store.go` 管任务实例（并发 + worktree 隔离）。

server/internal/fs       —— 「受管项目根目录」注册表（`registry.json`），同时封装文件树、共享
                             fsnotify watcher。所有项目本地数据落在 `<project>/.mindfs/` 下，
                             这是自托管承诺的物理载体。

server/internal/relay    —— 与 a9gent.com 中继服务的隧道（`hashicorp/yamux`），实现无公网 IP
                             的远程访问；`services.go` 处理「一键暴露本地服务」的转发。

server/internal/e2ee     —— 端到端加密：`config.go` 生成/读取 pairing secret，`manager.go` 维护
                             per-client 会话密钥。E2EE 打开后所有请求需带 `X-MindFS-*` 头（见
                             `api/http.go` 顶部常量），LAN 场景必须搭配 `-tls`。

server/internal/commandexec —— 前端「命令模式」的执行后端：`long_shell.go` 是持久 shell，
                             `runner.go` 是一次性运行，`limiter.go` 做并发限流，
                             `process_windows.go` / `process_unix.go` 处理平台差异（Windows 需要
                             显式改 OutputEncoding 为 UTF-8，见 agents.json 里 shell 配置）。

server/internal/scheduled —— 定时任务，基于 robfig/cron/v3。
server/internal/gitview   —— git status / history / diff 的服务化封装。
server/internal/webpush + notify + notifyscript —— PWA Web Push + 自定义 webhook 脚本。
server/internal/update    —— 自更新（校验签名 manifest 后覆盖二进制）。
server/internal/tlsutil   —— 自签证书生成/复用，SAN 覆盖 localhost + 全部非 loopback 网卡。
```

### 前端结构

```
web/src/App.tsx             —— 顶层 shell，负责 bootstrap、E2EE 握手、路由到 Renderer。
web/src/layout/AppShell.tsx —— 三栏/单栏切换、左右侧栏对调、移动端底部操作栏。
web/src/renderer/           —— viewCatalog + Renderer：每种文件类型（Markdown/代码/图片/二进制/
                              plugin 自定义）匹配一个 renderer。
web/src/services/           —— API 客户端。`base.ts` + `api.ts` 是核心；session/git/tasks/file
                              等按域拆分。`e2ee.ts` 负责密钥握手与请求签名，`connection.ts` 管
                              WebSocket 生命周期。
web/src/components/stream/  —— 流式会话的结构化卡片（ToolCallCard、ThinkingBlock）。
web/src/plugins/manager.ts  —— 插件系统：动态 import 项目里 `.mindfs/plugins/*.js`，接口约定
                              「传入文件内容 → 解析 → 渲染」（示例见根目录 plugins/txt-novel.js）。
web/src/hooks/useSessionStream.ts —— 订阅一条 session 的 WS 事件流的通用 hook。
```

关键构建约定：

- `VITE_APP_SHELL=1` 时会走「壳内运行」分支（无 Service Worker、走 nativeBridge），Android/Harmony
  的 build 脚本都会置位。
- `VITE_NATIVE_PLATFORM=harmony` 是 harmony 分支独有的运行时判断。
- 前端只做 `tsc --noEmit`，**没有 ESLint / Prettier 配置**，风格靠 review。

### 数据落盘位置

- 「受管项目列表」：`<userConfig>/mindfs/registry.json`（Linux 下 `~/.config/mindfs/`）。
- 每个项目自己的历史 / 元数据 / 视图配置：项目根下 `.mindfs/`（README 强调的「迁移只需复制」）。
- 自签 TLS 证书：与 registry 同目录。
- 用户偏好、e2ee pairing secret、web-push VAPID keys：同 `<userConfig>/mindfs/` 下。

## 协议 / 交互约定

- 前后端主要通信是 **JSON-RPC over WebSocket**（`web/src/services/connection.ts` ↔
  `server/internal/api/ws.go`）；REST 只覆盖幂等的 GET/上传/下载类接口。
- E2EE 打开后，REST 需带 `X-MindFS-E2EE` + `X-MindFS-Client-ID` + `X-MindFS-Proof` +
  `X-MindFS-TS`，WS 走 `?e2ee_proof=...&e2ee_ts=...` query 参数，`requestProofMaxSkew = 5min`。
- Agent 会话协议一律实现 `agent/types.Session` 接口，Pool 根据 `DefaultProtocol(name)` 或配置
  显式指定的 protocol 分派到 `acp/` / `claude/` / `codex/` runtime。想接入新 Agent，通常只需要
  在 `agents.json` 里加一条 `protocol: "acp"` 的定义，而不用改 Go 代码。
- Windows shell 用户在 `agents.json` 已经预置了 pwsh/powershell/bash.exe/wsl.exe/cmd.exe，pwsh
  和 powershell 的 `commandPrefix` 强制把 console encoding 改成 UTF-8，避免中文乱码；改动 shell
  相关代码时要保留这段。

## 发布流程注意事项

- `release-notes.md` 第一条必须是 `# MindFS vX.Y.Z`，`make publish-release-notes` / `make release`
  会用 sed 校验首行版本号；不匹配就直接拒绝。
- 发布产物签名密钥来自环境变量 `MINDFS_RELEASE_PRIVATE_KEY` 或 `MINDFS_RELEASE_PRIVATE_KEY_FILE`，
  校验用 `MINDFS_RELEASE_PUBLIC_KEY`。自更新（`mindfs -update`）依赖这套签名。
- `install.sh` / `install.ps1` 都从 GitHub raw 上的 `release-notes.md` 首行读版本号，因此发版
  前必须先 push 好这个文件（`make release` 会自动做 `publish-release-notes`）。

## 其它

- Go 1.25，前端 Node 20+，Android 需要 JDK 21（`ANDROID_JAVA_HOME` 会自动探测 macOS 的
  `java_home -v 21`）。
- `go.mod` 里对 `codex-go-sdk` / `acp-go-sdk` / `claude-agent-sdk-go` 都有 `replace` 指向
  `github.com/yandc/*` 的 fork —— 依赖升级时先跟这些 fork 对齐，不要直接指回上游。
- 桌面端支持通过 `-config config.json` 集中管理启动参数（示例见根目录 `config.json`），但
  命令行 flag 优先级最高（`applyStartupConfig` 里判断 `explicitFlags`）。



# Windows 本机部署

> 面向在本仓库上工作的 Agent。**动手部署前先读完**——这套流程是从多次失败里试出来的，
> 下面每条「坑」都真实发生过。不要自己发明部署方式。

## 一句话用法

```bash
# 1. 先构建（部署脚本不会帮你构建！）
npm --prefix web run build
PATH="/c/Go/bin:$PATH" go build -o mindfs.exe ./cli/cmd      # go 不在 PATH

# 2. 触发部署（不要直接跑 redeploy.ps1，见下）
schtasks /Run /TN MindFSRedeploy
```

脚本位于 `%LOCALAPPDATA%\Programs\mindfs\`（`redeploy.ps1` + `setup-tasks.ps1`）。
服务地址 `https://localhost:7766/`，自签证书，curl 要带 `-k`。

## 为什么必须走计划任务

用户常通过 MindFS 自己托管的会话来指挥 Agent，此时进程树是：

```
mindfs.exe → claude.exe / codex.exe → bash.exe（你）
```

停服务用的是 `taskkill /T`（树杀，见 `cli/cmd/mindfs_windows.go`），**你会跟着一起死**，
部署停在半路、服务起不来，用户得手动救场。

计划任务的宿主是 svchost，不在这棵树上，能在你被杀死后继续跑完。`MindFSRedeploy` 带 45 秒
延迟，够你把话说完再被杀；会话恢复后去看日志确认结果。

## 四个任务

| 任务 | 权限 | 触发 | 用途 |
|---|---|---|---|
| `MindFSStart` | 普通 | **登录时** | 开机自启；也是「普通权限启动」的委派目标 |
| `MindFSStartAdmin` | 管理员 | 手动 | 「管理员权限启动」的委派目标 |
| `MindFSRedeploy` | 管理员 | 手动 | **日常用这个**，部署后服务是普通权限 |
| `MindFSRedeployAdmin` | 管理员 | 手动 | 部署后服务是管理员权限 |

任务丢失或被改坏，**提权**跑一次重建（会自动校验每个任务的 RunLevel）：

```
powershell -NoProfile -ExecutionPolicy Bypass -File "%LOCALAPPDATA%\Programs\mindfs\setup-tasks.ps1"
```

## 权限模型

进程继承启动者的令牌，而任务的 RunLevel 在创建时就固定了，所以一个任务盖不住两种权限。
脚本的做法是**委派**：发现自身权限与 `-Privilege` 要求的不符，就转手触发权限正确的那个任务去启动。

- 部署入口是**管理员**权限——只有这样才停得掉可能存在的管理员权限老进程
- 但默认把**启动**委派给 `MindFSStart`，服务最终落在**普通权限**
- MindFS 不需要管理员权限（高位端口、数据都在用户目录下），而它跑的 agent 会继承它的权限，故默认不提权

被委派时会产生**两份**部署日志：主任务一份（停+换+委派），被委派任务一份（实际启动）。

想临时提权用 `MindFSRedeployAdmin`；之后跑一次普通部署、或重启电脑，就回到普通权限。

## 踩过的坑

| 现象 | 原因与对策 |
|---|---|
| 部署「成功」但代码没变 | **忘了先构建**。脚本只复制 `mindfs.exe` / `web\dist`，且不校验新旧，会拿旧产物覆盖上去还打印 `redeploy done`。失败会喊，成功地部署错东西不会喊——部署前比对源产物与已部署的大小/bundle hash |
| 停止超时、二进制换不掉 | 任务没加 `/RL HIGHEST`，普通权限杀不动管理员权限的进程。创建 HIGHEST 任务需提权，但**触发**已存在的不需要 |
| `stop: no running instance` 紧接着「文件正由另一进程使用」 | 进程检测漏了。要**端口优先**：`Get-CimInstance` 在计划任务令牌下可能返回空（实测 `-VerifyOnly` 报 `running instances: 0` 而同秒 health 为 True） |
| `ERROR: 无法终止 PID xxx` 部署中断 | `$ErrorActionPreference='Stop'` 下原生命令写 stderr 会抛 `NativeCommandError`，`2>&1 \| Out-Null` **拦不住**。用 `cmd /c "... 2>&1"` 隔离，成败以后续轮询为准 |
| 回滚也失败于文件占用 | 进程消失后 Windows 仍短暂持有句柄，需重试（`Copy-WithRetry`） |
| 桌面弹出终端窗口并常驻 | WMI 起 cmd 必须传 `ProcessStartupInformation` 且 `ShowWindow=0`(SW_HIDE) |
| 改完脚本报语法错误 | 脚本是 UTF-8 无 BOM，PowerShell 5.1 按 GBK 读，**中文注释会破坏语法**。注释一律写英文 |

## 日志

| 文件 | 内容 |
|---|---|
| `%AppData%\mindfs\logs\redeploy-<时间戳>.log` | 每次部署一份，保留最近 10 个 |
| `%AppData%\mindfs\logs\mindfs-service.log` | 服务 stdout/stderr，10MB × 3 代滚动 |

日志能存在全靠启动时的 `cmd >> 日志 2>&1` 重定向。服务日志的轮转只在重启时发生（运行期间
文件句柄被占着，Windows 不让改名），所以长期不重启就一直涨——按历史速度约 131 KB/天。

## 其它开关

```
-VerifyOnly       只检查环境，什么都不动
-StartOnly        不换文件，只重启
-EnsureRunning    已经健康就直接退出（登录任务用，避免重新登录时打断服务）
-Privilege        normal（默认）| admin
-DelaySeconds     动手前先等 N 秒
```

旧的 vbs 自启（`start-mindfs-hidden.vbs` + 已禁用的 `MindFS` 任务）留作后备。它用
`WScript.Shell.Run` 不重定向输出，所以那条路启动的服务**没有日志**——2026-07-13 到 08-15
之间日志断档就是这个原因。

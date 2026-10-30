# Gengar

基于命令行交互式 Linux 运维 AI Agent。

> **gengar-pro Alpha 公告：** 本项目已演进为内网团队自托管 Web 系统（SSH 直连多主机 + 单二进制 `gengar-server` + 浏览器访问）。旧的单机 CLI（`cmd/gengar`）与 Wails 桌面端均已退役。

## Alpha 使用指南（gengar-pro Web 端）

**构建/运行/部署**：见下文[构建与运行](#构建与运行)——开发（vite 热更新 + go run）与生产（单二进制 + systemd）分述。

**部署（内网服务器）**：启动/建用户命令与 systemd 常驻见下文[构建与运行](#构建与运行)的生产环境小节；密钥与边界语义如下。主密钥：默认 `<data>/secret.key`（首次自动生成）；容器部署可用 `GENGAR_MASTER_KEY`（hex 32 字节）注入。凭据（SSH 密码/私钥口令）AES-GCM 加密（AAD 绑定存储键）落盘 `gengar.db`——**数据目录权限即安全边界**。需要 TLS 时由前置反代（nginx/caddy）终接，服务本身只监听内网 HTTP。

> **终端复制/粘贴提示**：浏览器剪贴板 API 需安全上下文——本机访问用 `http://localhost:<port>`，跨机访问置于 TLS 反代之后；HTTP 内网 IP 下复制自动降级（execCommand）、粘贴走原生路径，右键菜单会提示。

**首次使用**：浏览器打开 `http://<server>:8080` → 内置管理员 `admin/admin` 登录（首次启动自动创建，请尽快改密）→ 设置页填 Anthropic API Key（团队共享一份，**仅 admin 可见可改**——密钥入加密存储）→ 服务器页添加主机（密钥或密码认证）→「测试连通」验证 → 开对话选定目标主机（可整目录多选）→ 直接输入自然语言（如「磁盘使用情况怎么样」）。多用户：admin 在侧栏「用户」管理弹窗建号/重置密码/删号（仅 admin 可见，删除用户时其名下主机自动移交 admin），每人各自登录，**主机清单按用户隔离**——每人只见/只操作自己名下的主机与目录（存量主机归 admin；admin 可见、可操作全部），对话里的 Agent 目标解析同样只解析自己名下的主机；对话按会话隔离——每个对话 tab 独立会话与目标（「＋」新开），不同登录（哪怕同账号）互不可见，浏览器刷新恢复原会话。同一主机终端 = 同一用户单一 PTY（该用户多端附加共享，他人不可见——会话按登录用户隔离，审计有记录）。

**自然语言目标**：指令里可用 主机名 / 标签（机组）/ `all` 指定目标；省略 hosts 时作用于**本次对话目标**——开对话时经选择器按目录分组勾选（一经选定本对话不可改，换范围 = 开新对话 tab），模型不会凭对话历史自行扩大范围。危险操作会弹确认框（附受影响主机清单），批准后才执行。

**内嵌终端**：侧栏主机行「>_」对主机开交互式 SSH 终端（主区标签页，可多开，与 Agent 任务互不阻塞）；「✎」编辑已录入主机（密码留空即保持原值）。对话流在等待与执行期间显示思考指示器和每步耗时。「导入」可上传本机 XShell Sessions 目录或多个 .xsh 文件批量导入（结构导入：名称/地址/端口/用户/目录→目录与标签；密码不导入）。侧栏主机清单支持目录树组织：主机的「目录」字段（如 `生产`，留空为根）决定层级，也是对话目标选择器的分组单位。右键空白处可新建目录（随后弹出预填目录的添加主机表单）、右键目录可添加主机或删除目录（目录内主机移回根）；主机行可直接拖拽进目录或拖到空白处移回根。

**刷新与重连**：对话事件服务端按会话缓冲（约 2000 条/对话 tab），浏览器刷新/断线自动重连并重放（序号断档自动全量重排；服务重启后旧会话自动复活为空会话；断线期间顶部显示「正在重连」横幅）。确认/提问弹窗在等待期间断开浏览器 = 按拒绝/取消处理（不会卡死任务）；决议发送失败弹窗保持可重试。

**headless 测试入口**：`go run ./cmd/gengar-headless --data <dir> --secrets ref=pass --pin-all --yes`，
配合管道输入即可脱离 Web 验证产品逻辑（`--yes` 自动批准 Risky）。

**数据目录**：`--data` 指定（默认 `~/.gengar-pro/`）— `gengar.db`（主机清单/设置/用户/加密凭据，WAL 模式）、`memory/`（每主机记忆 JSON）、`audit-YYYY-MM-DD.jsonl`（执行审计按天分片，保留 30 天，含操作者用户名）、`known_hosts`（SSH TOFU 主机密钥）、`keys/`（上传的 SSH 私钥，按主机 ID 命名）、`secret.key`（凭据主密钥）。

**资源回收**：登录令牌 7 天过期（每小时清扫）；登录失败连续触发会临时限速；闲置 30 分钟无浏览器连接的对话会话与闲置 1 小时无人观看的终端会被服务端自动回收（重新打开即恢复）；`GET /api/health` 无鉴权探活（含审计写失败计数，供监控告警）。

**已知限制（alpha）**：会话不持久化（服务重启即新会话，浏览器刷新不受影响）；设置变更会重置全部用户对话（有提示帧）；交互式 `--add-user` 输入有回显；集成测试需 Docker（`go test -tags integration ./internal/integration/`）。

**人工验收清单**（Alpha 收尾自查）：

1. 首次启动自动创建 admin/admin；错误密码登录被拒；连续错密码触发限速提示；登出后回登录页；admin 在「用户」弹窗建用户/重置密码（旧会话吊销）/删号，非 admin 无此入口且 API 403；多用户隔离：alice/bob 各建主机互不可见（列表/编辑/删除/终端均按属主 404，admin 全见可操作）；删除 bob 后其名下主机移交 admin
2. 设置页保存 API Key（Base URL / 模型可选），列表掩码显示；非 admin 无设置入口且 API 403（key 落 secrets 加密表，不再明文入 settings 表）；保存后其他用户对话收到「已重置」提示
3. 添加服务器：密钥认证（私钥文件上传，表单「测试」可先试连）与 密码认证 各一台（引用服务器本地路径作私钥被 400 拒绝）
4. 「测试连通」返回 ok，侧栏状态点亮
5. 开对话选定目标（目录整组/多选/全部）：省略主机的指令只作用于目标；目标一经选定本会话固定（换范围 = 开新对话 tab，选择器仅拦本对话区不挡 tab 栏）
6. 模糊意图触发 ask_user 提问弹窗，选择后继续
7. Risky 操作弹确认：批准后执行、拒绝后不执行；批准失败弹窗保持并提示
8. 等待确认期间刷新浏览器：重放后弹窗重现，决议继续
9. Esc 取消进行中的任务，输入框恢复可用，取消后会话可继续对话（不报废）
10. 斜杠命令触发 Skill（/inspect、/install）；未知斜杠输入原样交给 Agent
11. 重启服务后主机清单/设置/用户仍在；会话清空（预期）
12. 同一主机同一账号两个浏览器开终端：共享同一会话；不同账号各自独立 PTY；关标签不中断，× 才断开；attach 前的输出经回放缓冲自动补发
13. XShell 上传导入 → 预览（重复灰置）→ 提交；audit 分片各条含 user 字段；curl/wget/env 及含非白名单段的链式命令需确认后才执行（纯只读链式组合仍免确认）

## 概述

Gengar 用自然语言接收运维意图，翻译成 shell 命令并经 SSH 在受管主机上执行。支持多步推理的故障诊断和服务部署。

**核心特征：**

- Go 语言实现，单二进制分发，零外部依赖
- 命令行对话式交互，ReAct（推理-行动）循环
- 10 个运维专用 Plugin + 通用 shell 执行兜底（35 个工具）
- 内置 Skill 体系，斜杠命令触发专家 Playbook（如 `/inspect` 服务器巡检、`/install` 软件安装）
- 2 级安全确认机制（Safe 自动执行 / Risky 需用户确认）
- 会话内上下文保持，长输出自动截断
- 支持 Profile 环境共识，让 Agent 了解环境特定配置和规范
- 服务器记忆持久化，跨会话保持上下文
- 结构化需求澄清（ask_user），Agent 可主动向用户提问

## 架构

```
User Input (Natural Language)
        |
        v
+-------------------------------------------------------+
|                      ReAct Loop                       |
|                                                       |
|   +------------+    +------------+    +------------+  |
|   |  Claude    | -> |  Safety    | -> |  Executor  |  |
|   |  API       |    |  Gate      |    |            |  |
|   | (tool use) | <- |            | <- |            |  |
|   +------------+    +------------+    +------------+  |
|         |                                             |
|         v                                             |
|   Task Done?  --No-->  Continue Loop                  |
|         |                                             |
|        Yes                                            |
|         v                                             |
|   Return Final Result to User                         |
+-------------------------------------------------------+
```

## Plugin 体系

| Plugin | Tools | 说明 |
|--------|-------|------|
| ShellExecutor | `execute_command` | 通用 shell 命令执行，兜底能力（仅在其他专用工具不适用时使用） |
| FileManager | `read_file`, `write_file`, `list_directory`, `find_files`, `grep_files` | 文件读写、目录浏览、文件搜索与内容检索 |
| DockerManager | `docker_ps`, `docker_logs`, `docker_compose_up`, `docker_compose_down`, `docker_exec` | Docker/Compose 操作 |
| SystemdManager | `systemctl_status`, `systemctl_start`, `systemctl_stop`, `systemctl_restart`, `journalctl_logs` | Systemd 服务管理 |
| PackageManager | `package_install`, `package_remove`, `package_search`, `package_list` | 自动检测 apt/yum/dnf |
| DiagnosticKit | `check_port`, `check_process`, `check_disk`, `check_memory`, `check_network`, `tail_log`, `system_info` | 故障排查专用工具集 + 系统信息采集 |
| NetworkPlugin | `ping`, `dns_lookup`, `http_check` | 网络连通性检测（ping / DNS / HTTP） |
| KnowledgeBase | `query_consensus` | 查询当前环境的基础设施事实和运维规范（基于 Profile） |
| MemoryManager | `memory_read`, `memory_write`, `memory_delete` | 服务器记忆持久化（OS 基本信息 + 历史操作记录） |
| ClarifyPlugin | `ask_user` | 结构化需求澄清，Agent 主动向用户提问 |

## 安全分级

采用 **2 级安全模型**：非黑即白，任何不被证明安全的操作都需要确认。

| Level | 值 | 含义 | 行为 |
|-------|----|------|------|
| Safe | 0 | 只读 / 安全 | 自动执行，不打扰用户 |
| Risky | 1 | 有副作用 / 不可证明安全 | 必须由用户确认后才执行 |

安全规则内置于代码中，包含白名单、黑名单与受保护路径。命令按 shell 运算符（`;` `|` `&&` `||`）拆段**逐段**判定：每段都命中白名单整串才免确认——`cat x; rm -rf /data` 这类「白名单命令夹带删除」的绕过形态一律弹确认；含命令替换（`$(...)`/反引号）或重定向（`>`/`>>`）直接确认。链式只读组合（`df -h; free -m`、`ps aux|grep nginx`）仍免确认。`curl`/`wget`/`env` 不在免确认名单内。插件工具的风险分级以插件声明为单一真相源（组装时注入安全门）。

### 批量确认

同一轮 ReAct 步骤（单次 API 响应）里产生的**所有 Risky 命令合并到同一个确认弹窗**，而非逐条打断。弹窗以截断摘要的形式列出待执行命令，用户一次决议（批准/拒绝）应用到整批：

- 普通 Risky：默认聚焦「Yes」，直接回车即批准
- 命中黑名单（`rm -rf /`、`reboot`、`shutdown` 等）的高风险命令：弹窗标题变为「⚠️ 高风险操作」，必须显式输入 `yes` 才放行（不接受默认 Enter）；只要批内含任意高风险命令，整批都按严格模式校验

## 需求澄清（ask_user）

当用户意图不明确或存在多种理解方式时，Agent 可通过 `ask_user` 工具主动提问。提问包含结构化的选项列表，用户选择编号或输入自定义回答。

典型场景：
- 操作目标不明确（如「清理日志」→ 压缩？删除？清空？）
- 影响范围不明确（如「重启服务」→ 哪台机器？）
- 优先级不明确（如「优化性能」→ 优先速度还是稳定性？）

## Skill 体系

Gengar 的第三种扩展点。与提供工具的 Plugin、注入静态事实的 Profile 共识不同，Skill **不新增工具**，而是在被斜杠命令触发时，把一段专家 Playbook（过程性流程指南）注入对话，指导 Agent 如何组合现有工具完成一类任务。

### 工作原理

- Skill 文件为 Markdown + YAML frontmatter，编译期通过 `go:embed` 嵌入二进制（零外部依赖）
- 每个 Skill 绑定一个斜杠命令（如 `/inspect`）
- 触发时打印加载提示，并把 Playbook 作为结构化指令注入，复用现有 ReAct 循环与安全门控

### 内置 Skill

| Skill | 命令 | 说明 |
|-------|------|------|
| 服务器巡检 | `/inspect` | 系统性健康检查：资源水位（CPU/内存/磁盘/负载）、服务健康、网络连通、安全审计，对话输出 Markdown 汇总 |
| 软件安装 | `/install` | 按发行版选包管理器（apt/yum/dnf/pacman/apk/zypper）、按环境适配镜像源与 HTTP 代理、异常处理、安装后验证并按需起服务，对话输出 Markdown 汇总（别名 `/inst`、`/pkg`） |

### 示例

```bash
> /inspect
加载 Skill: 服务器巡检 — 系统性健康检查
（Agent 逐项检查并汇总健康状态）

> /install nginx
加载 Skill: 软件安装 — 按发行版选包管理器、适配源与代理、安装后验证并按需起服务
（Agent 探测发行版与源、按需换源/走代理、确认后安装、验证、按需起服务并汇总）
```

## Profile 环境共识

Gengar 支持基于 Profile 的环境共识机制，让 Agent 了解不同环境的基础设施事实和运维规范。

### 工作原理

- 启动时根据 `GENGAR_ENV` 加载对应的 Profile（默认 dev）
- Profile 数据编译期嵌入二进制（通过 `go:embed`），零外部依赖
- Agent 系统提示词自动注入该 Profile 的共识目录，告知有哪些可查阅条目
- Agent 可通过 `query_consensus(topic=<id>)` 工具查询具体条目内容

### 可用 Profile

| Profile | 说明 |
|---------|------|
| `dev` | 开发环境（默认） |
| `prod` | 生产环境 |

### 共识条目类型

- **基础设施事实**：YUM 源地址、NTP 服务器、DNS 配置等
- **运维规范**：备份策略、服务命名约定、部署流程等

### 示例

```bash
# 生产环境启动
export GENGAR_ENV=prod
./gengar

# Agent 系统提示词会自动注入：
# === 环境共识目录 (profile: prod) ===
# 当前环境有以下可查阅的运维规范与基础设施事实...
```

## 环境变量配置

| 环境变量 | 必填 | 默认值 | 说明 |
|---------|------|--------|------|
| `ANTHROPIC_API_KEY` | Web 端否¹ | — | Anthropic API Key |

¹ Web 端可改由设置页保存（api_key 入 secrets 加密通道）或首次启动时从 `~/.claude/settings.json` 自动导入（优先级：环境变量 > secrets/settings > 引导 > 默认值；设置端点仅 admin）。
| `ANTHROPIC_BASE_URL` | 否 | `https://api.anthropic.com` | API 基础 URL |
| `ANTHROPIC_MODEL` | 否 | `claude-sonnet-4-20250514` | 使用的 Claude 模型 |
| `GENGAR_ENV` | 否 | `dev` | 环境共识 Profile（dev/prod） |

## 服务器记忆（Memory）

Gengar 把每台受管服务器的基本信息和重要操作记录持久化到 `~/.gengar-pro/memory/<host>.json`（按主机分域，`--data` 可改根目录），让 Agent 跨会话保持上下文，避免重复探测。

### 分类

- system_info：操作系统、内核、CPU、内存、磁盘等基本信息
- history：Agent 执行过的关键操作（安装、配置变更等）

### 工作机制

- 启动时自动加载（文件不存在或损坏不影响启动）
- Agent 根据 system prompt 中的使用指南，按需调用 `memory_read` / `memory_write`

### 手动清空

```bash
rm ~/.gengar-pro/memory/<host>.json
```

## 使用

日常使用即浏览器访问 Web 端（见上文「Alpha 使用指南」）。自动化/管道场景走 headless 入口：

```bash
echo "帮我看看磁盘使用情况" | \
  go run ./cmd/gengar-headless --data ~/.gengar-pro --secrets ref=pass --pin-all --yes
```

`--yes` 自动批准全部 Risky 确认，斜杠命令（含 Skill，如 `/inspect`）同样生效，统一经 Registry 路由，适合脚本与 E2E 测试。

### 斜杠命令（Skill 触发）

斜杠命令仅用于触发 Skill（内置命令 /help、/quit、/clear、/history 已随 CLI 形态退役移除；未注册的斜杠输入原样交给 Agent 处理）：

| 命令 | 说明 |
|------|------|
| `/inspect` | 服务器巡检 Skill（系统性健康检查，别名 `/inspection`、`/healthcheck`） |
| `/install` | 软件安装 Skill（按发行版选包管理器、适配源与代理、装后验证并按需起服务，别名 `/inst`、`/pkg`） |

## 项目结构

```
gengar-pro/
├── main.go                         # Web 服务入口（embed 前端 / 装配 EncryptedStore / --add-user / 内置 admin 种子）
├── frontend/                       # React + Vite 前端（构建产物 dist 由 go:embed 内嵌）
│   ├── src/
│   │   ├── App.tsx                 # 应用骨架（登录门 + 侧栏 + 对话/终端标签页、事件归约接线）
│   │   ├── main.tsx                # React 入口
│   │   ├── api/
│   │   │   ├── client.ts           # REST 封装（函数对齐端点形状；401 统一切回登录页）
│   │   │   └── events.ts           # WS 订阅层（自动重连 + since 续传 + 断档全量重排 + 心跳）
│   │   ├── timing.ts               # 计时纯函数层（事件归约器 / 思考残留 / 工具耗时 / 轮尾统计）
│   │   ├── form.ts                 # 表单纯函数（标签解析）
│   │   ├── folders.ts              # 侧栏目录树纯函数（buildTree / allFolderPaths）
│   │   ├── components/             # Login / Chat / InputBar / TerminalPane / ConfirmModal / AskModal / HostForm / ImportDialog / Settings / UsersDialog / Sidebar
│   │   ├── styles.css
│   │   └── types.ts
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts              # dev 模式 /api 代理（含 WS）到 127.0.0.1:8080
├── cmd/
│   └── gengar-headless/main.go     # 无 Web 验证入口（stdin 驱动 Agent 循环，测试设施）
├── internal/
│   ├── webapi/                     # Web 服务层（登录会话 + REST + WS，向下调 appcore）
│   │   ├── server.go               # 路由装配 + 每用户状态表（设置保存后重建全部 Agent）
│   │   ├── auth.go                 # 登录/登出/me（bcrypt 校验 + cookie 会话令牌，内存态）
│   │   ├── users.go                # 用户管理 REST（仅 admin：列表/创建/重置密码/删除，会话即时吊销）
│   │   ├── hosts.go                # 主机管理 REST（增删改 / 测试连通 / 对话目标 scope）
│   │   ├── settings.go             # 全局设置 REST（团队共享一份，掩码回显）
│   │   ├── chat.go                 # 对话端点（提交/取消/确认与提问决议回填）+ 对话 WS 事件流
│   │   ├── hub.go                  # 每用户事件枢纽（有界 FIFO 缓冲 + 按序号重连重放 + fan-out）
│   │   ├── sink.go                 # ui.Sink 的 web 实现（确认/提问阻塞等待 REST 决议；断连按拒绝/取消放行）
│   │   ├── termws.go               # 终端端点（REST 开/关 + WS 双向流，多端附加同一 PTY）
│   │   ├── xshell.go               # XShell 上传导入（base64 上传 → 解析预览 → 勾选提交）
│   │   ├── state.go                # 每用户会话状态装配（agent/hub/sink + 单任务锁）
│   │   ├── json.go                 # JSON 请求/响应读写小工具
│   │   └── static.go               # 前端产物静态托管（SPA fallback；embed 由模块根注入）
│   ├── agent/
│   │   ├── agent.go                # ReAct 循环核心（多主机提示词 / 批量确认 / 审计 / ask_user 处理）
│   │   ├── conversation.go         # 会话上下文管理（含 SetSystemPrompt）
│   │   └── token.go                # Token 估算与压缩判断
│   ├── appcore/
│   │   ├── core.go                 # 组装层（清单+执行+钉住+目标解析+BuildAgent，headless 与 Web 入口共用）
│   │   └── session.go              # 单用户视图（钉住/默认目标按用户隔离 + BuildAgent），webapi 每登录用户一个
│   ├── audit/
│   │   └── audit.go                # 操作审计流水（JSONL 按天分片轮转，每条 fsync，含操作者 user；写失败计数暴露 /api/health）
│   ├── host/
│   │   ├── host.go                 # Host / AuthConfig 数据模型（含 Folder 目录字段）
│   │   ├── folder.go               # 目录路径规范化（NormalizeFolder）
│   │   ├── resolve.go              # 目标引用解析（主机名 / 标签 / all）
│   │   ├── store.go                # SQLite 清单 / 设置 KV / 用户表
│   │   ├── secrets.go              # SecretStore 接口（内存实现 / 环境变量解析，测试与 headless 用）
│   │   └── encrypted.go            # EncryptedStore（AES-GCM 加密凭据入库，AAD 绑定 ref）+ 主密钥加载
│   ├── executor/
│   │   ├── executor.go             # Executor 接口 + Local 实现
│   │   ├── ssh.go                  # SSH 执行器（session 执行 / SFTP 写文件 / argv 单引号引用）
│   │   ├── knownhosts.go           # TOFU 主机密钥校验
│   │   └── manager.go              # 按主机缓存 SSH 连接（探活看门狗 + 死连接重拨）
│   ├── term/
│   │   ├── term.go                 # 内嵌交互终端：专属连接 PTY 会话（输出 base64 事件 / 写入互斥 / 按主机关闭）
│   │   └── term_test.go            # 进程内 SSH 假服务器 + 回显/尺寸/并发用例
│   ├── xshell/
│   │   ├── xshell.go               # XShell .xsh 会话解析（UTF-16LE INI / 文件夹→目录与标签 / 容错 / 重名加后缀）
│   │   └── xshell_test.go          # 编码兜底/跳过与失败计数/端口与文件名防护等用例
│   ├── config/
│   │   ├── config.go               # 全局配置（API Key/BaseURL/模型/超时），从环境变量加载
│   │   └── claude.go               # 解析 ~/.claude/settings.json 的 env 对象（启动引导用）
│   ├── command/
│   │   └── command.go              # 内置斜杠命令（/help /quit /clear /history）+ Registry 注册表（名称/别名/前缀匹配，Skill 命令亦注入此处）
│   ├── claude/
│   │   ├── types.go                # Claude API 类型定义
│   │   └── client.go               # HTTP 客户端（含重试）
│   ├── safety/
│   │   ├── level.go                # 2 级安全等级（Safe/Risky）
│   │   ├── gate.go                 # 动态命令分析引擎
│   │   └── rules.go                # 内置安全规则（黑白名单）
│   ├── skill/
│   │   ├── skill.go                # Skill 类型、frontmatter 解析、加载、ToCommand、Prompt
│   │   └── data/
│   │       ├── inspect.md          # 服务器巡检 Skill（go:embed）
│   │       └── install.md          # 软件安装 Skill（go:embed）
│   ├── plugin/
│   │   ├── types.go                # Plugin 接口与 ToolDefinition（Targeting 标记）/ ToolResult（Hosts）
│   │   ├── registry.go             # Plugin 注册中心（目标解析 + 按主机 fan-out 与聚合）
│   │   ├── shell.go                # ShellExecutor
│   │   ├── file.go                 # FileManager（含 find_files / grep_files）
│   │   ├── docker.go               # DockerManager
│   │   ├── systemd.go              # SystemdManager
│   │   ├── pkgmgr.go               # PackageManager
│   │   ├── diagnostic.go           # DiagnosticKit（含 system_info）
│   │   ├── network.go              # NetworkPlugin（ping / dns_lookup / http_check）
│   │   ├── knowledge.go            # KnowledgeBase（环境共识查询）
│   │   ├── memory.go               # MemoryManager（按主机记忆工具集）
│   │   └── clarify.go              # ClarifyPlugin（需求澄清）
│   ├── memory/
│   │   └── memory.go               # 按主机分域的记忆存储（memory/<host>.json）
│   ├── consensus/
│   │   ├── consensus.go            # 环境共识加载与查询
│   │   └── data/                   # Profile YAML 数据（嵌入二进制）
│   │       ├── dev.yaml
│   │       └── prod.yaml
│   ├── observability/
│   │   └── e2elog.go               # 结构化事件日志（JSONL）
│   ├── ui/
│   │   ├── sink.go                 # Sink 接口与 ConfirmRequest（含 Hosts）/ ConfirmAutoApprove
│   │   ├── spinner.go              # FormatToolDisplay（工具调用展示名）
│   │   ├── truncate.go             # TruncateMiddle（确认摘要截断）/ FormatConfirmSummary
│   │   ├── writer.go               # NewLineWriter 行缓冲写入适配器
│   │   └── plain/                  # 非 TTY 输出（headless / 测试，字节级稳定）
│   ├── integration/                # Docker sshd 集成测试（go:build integration 隔离）
│   │   ├── integration_test.go     # SSHExecutor / fan-out / Names≡Resolve 真连接断言
│   │   └── sshd.Dockerfile         # 最小 sshd 目标机镜像
│   └── e2e/                        # E2E 测试框架（go:build e2e 隔离：桩 LLM + Docker sshd + headless 驱动）
│       ├── e2e_test.go             # 测试入口
│       ├── cases.go                # YAML 用例 schema / 加载 / 断言求值
│       ├── harness.go              # 驱动编排（构建并运行 headless、读 audit 与记忆文件断言）
│       ├── sshd.go                 # Docker sshd 目标机容器
│       ├── stubllm.go              # 桩 LLM 服务（SSE 脚本回放）
│       ├── README.md               # E2E 运行说明
│       └── testcases/              # YAML 测试用例（E01-E05 / R01）
├── site/                           # 旧版官网静态资源（未随 Web 端更新）
├── go.mod
└── go.sum
```

## 构建与运行

### 开发环境

前置：Go 1.26+、Node 18+；npm 一律加 `--legacy-peer-deps`。

```bash
# 1) 前端依赖（首次）
cd frontend && npm install --legacy-peer-deps

# 2) 构建一次前端产物——go:embed 的输入，缺了 go build/run 直接报错
npm run build

# 3) 起后端（API + 内嵌前端 + WS，监听 :8080）
cd ..
go run . --data /tmp/gengar-dev --listen 127.0.0.1:8080

# 4) 建登录用户（可选；内置 admin/admin 已可直接登录，Web「用户」弹窗亦可建号）
GENGAR_NEW_PASSWORD=dev123 go run . --data /tmp/gengar-dev --add-user alice

# 5) 前端热更新（可选，另开终端）：vite :5173，/api（含 WS）已代理到 8080
cd frontend && npm run dev
```

说明：步骤 2 只需首次与前端依赖变更后执行——日常迭代走步骤 5 的 vite 热更新（改 Go 代码才需重启步骤 3）；不经 vite 直开 `http://127.0.0.1:8080` 用的是步骤 2 的内嵌快照。数据目录 `/tmp/gengar-dev` 可随意换/删（重置全部状态）。

### 生产环境（内网服务器）

```bash
# 构建机上：两步产物单二进制（约 20MB，内嵌前端与 SQLite，无外部文件依赖）
cd frontend && npm run build
cd ..
go build -o gengar-server .

# 交叉编译 Linux（在 Windows 构建机上）
# PowerShell:  cd frontend; npm run build; cd ..
#              $env:GOOS="linux"; $env:GOARCH="amd64"; go build -o gengar-server .

# 部署：拷贝 gengar-server 单文件到服务器，然后
./gengar-server --data /opt/gengar --listen :8080                             # 首跑自动生成 secret.key（0600）+ 内置管理员 admin/admin
GENGAR_NEW_PASSWORD=*** ./gengar-server --data /opt/gengar --add-user alice   # 建用户（可选；Web 用户管理亦可）
```

**升级**：替换二进制重启即可——主机清单/设置/用户/凭据都在 `--data` 目录，会话按已知限制清空。

**systemd 常驻**（可选）：

```ini
# /etc/systemd/system/gengar.service
[Unit]
Description=gengar-pro web server
After=network.target

[Service]
ExecStart=/opt/gengar/gengar-server --data /opt/gengar --listen :8080
Restart=on-failure
# 主密钥改用注入时取消注释（hex 32 字节）：
# Environment=GENGAR_MASTER_KEY=...

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now gengar
```

### 运行测试

```bash
go test ./...                        # 单元测试（不加 -race）
cd frontend && npm test              # 前端单测（vitest）
go test -tags integration ./internal/integration/   # Docker sshd 集成测试
# E2E 详见 internal/e2e/README.md
```

## 技术栈

- Go 1.26+（标准库 net/http 方法路由 + gorilla/websocket + modernc.org/sqlite 纯 Go 驱动）
- Claude Messages API (HTTP + SSE streaming)
- React 18 + Vite + xterm.js + react-markdown（浏览器端，go:embed 内嵌）
- 外部依赖：`gopkg.in/yaml.v3`、`golang.org/x/crypto`（bcrypt/AES-GCM/ssh）、`github.com/gorilla/websocket`、`github.com/pkg/sftp`

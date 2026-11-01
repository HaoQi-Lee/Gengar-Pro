# Gengar-Pro

内网团队自托管的 Linux 运维 AI Agent Web 系统：单二进制 `gengar-server`（Go + go:embed 内嵌前端 + SQLite，零外部依赖）+ 浏览器访问，SSH 直连多主机，自然语言驱动运维。

> 旧的单机 CLI（`cmd/gengar`）与 Wails 桌面端均已退役；`cmd/gengar-headless` 保留为无 Web 验证/测试入口。

## 核心特性

- **单二进制自托管**：Go 实现，前端产物与共识/Skill 数据编译期内嵌，部署即拷贝一个文件；SQLite 持久化（纯 Go 驱动，无 CGO）
- **多用户 Web 系统**：用户名/密码登录（bcrypt + cookie 会话），admin 管理用户与全局设置；**主机清单与对话会话按用户隔离**（普通用户只见/只操作自己名下的主机，admin 全见）
- **SSH 直连受管主机，远端零部署**：命令执行 / SFTP 写文件 / 交互 PTY 终端三种通道；连接缓存 + 探活看门狗 + TOFU 主机密钥
- **自然语言 → ReAct 循环**：Claude Messages API **SSE 流式**逐 token 输出，多步推理；长会话自动上下文压缩，Esc 取消不丢已生成文本
- **多主机目标模型**：指令里用 主机名 / 标签（机组）/ `all` 指定目标，省略则作用于本次对话选定的目标；目标型工具按主机 fan-out（8 并发）聚合
- **10 个运维插件 35 个工具** + 斜杠命令 Skill（`/inspect` 巡检、`/install` 软件安装）
- **2 级安全模型**：命令按 shell 运算符拆段逐段判定，Risky 批量确认，高风险命令严格模式；全部操作落审计流水
- **凭据服务端加密**：SSH 密码/私钥口令 AES-256-GCM 加密落盘（AAD 绑定引用），主密钥文件或环境注入
- **内嵌交互终端**：浏览器直开主机 SSH 终端（xterm.js），同主机同用户多端附加共享单一 PTY，attach 自动补发回放缓冲
- **XShell 会话批量导入**：上传 Sessions 目录或多个 `.xsh` 文件，结构导入（密码不导入）
- **环境共识与服务器记忆**：Profile 化的基础设施事实/运维规范注入提示词；按主机的跨会话记忆

## 架构总览

```
浏览器（React SPA：登录 / 主机侧栏 / 对话 tab / 内嵌终端）
  │ REST  登录·主机·设置·对话·用户·XShell 导入·健康探活
  │ WS    对话事件流（按会话缓冲，断线重连重放）· 终端 PTY 流
  ▼
gengar-server（单二进制 Web 服务）
  ├─ webapi        路由/登录会话/REST/WS；每对话 tab 一个 chatSession（目标·事件缓冲·Agent·任务锁按会话隔离）
  ├─ appcore       组装层（Web 与 headless 两入口共用）；Session 按用户视角隔离清单与默认目标
  │   ├─ agent     ReAct 循环：Claude SSE 流式 → 安全门批量确认 → 工具执行 → 结果回注
  │   │   ├─ plugin Registry   10 插件 35 工具；目标型工具解析引用后按主机 fan-out 聚合
  │   │   ├─ safety Gate       Safe/Risky 二级分级 + 命令分段判定
  │   │   ├─ memory            按主机记忆（memory/<host>.json）
  │   │   └─ consensus         环境共识（Profile YAML，嵌入二进制）
  │   ├─ executor.Manager      SSH 连接缓存 + 探活看门狗 + 死连接重拨 + TOFU known_hosts
  │   └─ host.Store            SQLite（主机/设置/用户）+ EncryptedStore（AES-GCM 凭据密文）
  ├─ term.Manager  交互终端：按「主机+属主」专属 PTY 连接，64KB 回放缓冲，多端共享
  └─ audit         操作审计 JSONL，按天分片，每条 fsync
  │ SSH（命令执行 / SFTP / PTY）
  ▼
受管 Linux 主机 × N（零部署，只需 SSH 可达）
```

Agent 核心是一个流式 ReAct（推理-行动）循环：

```
+------------------------------------------------------------------+
|  用户自然语言 → Claude API（SSE 流式，逐 token 渲染）              |
|       ↓ 工具调用                                                  |
|  安全门分级 → Risky 批量确认（一个弹窗决议整批）                    |
|       ↓                                                          |
|  plugin Registry：目标引用 → 主机名（Names ≡ Resolve 对称）         |
|       → 按主机 fan-out 执行（Executor 本地/SSH） → 聚合结果         |
|       ↓                                                          |
|  结果回注对话上下文 → 未 end_turn 则继续下一步                      |
|  （每步前检查上下文压缩；取消时补写 tool_result 防会话报废）         |
+------------------------------------------------------------------+
```

各包职责的权威说明见各包 `SPEC.md`（psyduck 规范生命周期）；设计文档归档于 `.psy/`。

## 快速开始

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

**升级**：替换二进制重启即可——主机清单/设置/用户/凭据都在 `--data` 目录，会话按已知限制清空。SIGINT/SIGTERM 优雅退出（3s 排空在途请求后关 PTY 与存储；再按一次 Ctrl-C 强退）。

**TLS 边界**：服务本身只监听内网 HTTP；需要 TLS 时由前置反代（nginx/caddy）终接。

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

## Web 使用指南

### 首次使用

浏览器打开 `http://<server>:8080` → 内置管理员 `admin/admin` 登录（首次启动自动创建，请尽快改密）→ 设置页填 Anthropic API Key（团队共享一份，**仅 admin 可见可改**——密钥入加密存储）→ 服务器页添加主机（密钥或密码认证）→「测试连通」验证 → 开对话选定目标主机（可整目录多选）→ 直接输入自然语言（如「磁盘使用情况怎么样」）。

多用户：admin 在侧栏「用户」管理弹窗建号/重置密码/删号（仅 admin 可见，删除用户时其名下主机自动移交 admin），每人各自登录。

> **终端复制/粘贴提示**：浏览器剪贴板 API 需安全上下文——本机访问用 `http://localhost:<port>`，跨机访问置于 TLS 反代之后；HTTP 内网 IP 下复制自动降级（execCommand）、粘贴走原生路径，右键菜单会提示。

### 多用户与数据隔离

- **主机清单按属主隔离**：普通用户只见/只操作自己名下的主机与目录（列表/编辑/删除/终端/导入判重均按属主，他人主机等价不存在 → 404，防存在性探测）；admin 可见、可操作全部（存量主机归 admin）。对话里的 Agent 目标解析同样只解析自己名下的主机——Agent 看不到他人的主机名
- **对话按会话隔离**：每个对话 tab 独立会话与目标（「＋」新开），不同登录（哪怕同账号）互不可见，浏览器刷新恢复原会话
- **终端会话隔离**：同一主机 = 同一用户单一 PTY（该用户多端附加共享，他人不可见），审计有记录
- **角色管理**：用户角色创建后不可变（admin / 普通用户）；重置密码或删除用户会即时吊销其全部登录令牌（强制重登录）

### 主机管理

- 添加主机：密钥认证（私钥文件上传，服务端落盘 `keys/<hostID>.key` 并解析验证；引用服务器本地路径作私钥会被 400 拒绝）或密码认证；表单内「测试」可先用未保存参数直连探测（不落库）
- 「✎」编辑已录入主机（密码留空即保持原值）；「测试连通」成功会采集 OS 回写侧栏状态点
- 侧栏支持**目录树组织**：主机的「目录」字段（如 `生产`，留空为根）决定层级，也是对话目标选择器的分组单位。右键空白处可新建目录（随后弹出预填目录的添加主机表单）、右键目录可添加主机或删除目录（目录内主机移回根）；主机行可直接拖拽进目录或拖到空白处移回根
- **XShell 导入**：上传本机 XShell Sessions 目录（保留层级）或多个 `.xsh` 文件批量导入——结构导入（名称/地址/端口/用户/目录→目录与标签；重复灰置、重名自动加后缀；**密码不导入**）

### 对话与目标（scope）

- 开对话时经选择器按目录分组勾选目标（目录整组/多选/全部）；**一经选定本对话不可改**，换范围 = 开新对话 tab（选择器仅遮罩本对话区，不挡 tab 栏）
- 指令里可用 主机名 / 标签（机组）/ `all` 显式指定目标；省略 hosts 时作用于本次对话选定的目标——模型不会凭对话历史自行扩大范围
- 确认弹窗展示的受影响主机清单与实际执行主机恒等（Names ≡ Resolve 对称不变量）

### 安全确认与需求澄清

- **Risky 操作批量确认**：同一轮 ReAct 步骤里的所有 Risky 命令合并到同一个确认弹窗（附受影响主机清单与截断摘要），一次决议（批准/拒绝）应用到整批
- 普通 Risky：默认聚焦「Yes」，直接回车即批准；**命中黑名单**（`rm -rf /`、`reboot`、`shutdown` 等）的高风险命令：弹窗标题变为「⚠️ 高风险操作」，必须显式输入 `yes` 才放行——批内含任一高风险则整批走严格模式
- **ask_user 需求澄清**：意图模糊时（如「清理日志」→ 压缩？删除？清空？）Agent 主动弹结构化提问，选择或自定义回答后继续
- Esc 取消进行中的任务：已生成的部分文本保留，会话可继续对话（不报废）

### 内嵌 SSH 终端

侧栏主机行「>_」对主机开交互式 SSH 终端（主区标签页，可多开，与 Agent 任务互不阻塞）。多端附加共享同一 PTY；关标签页不中断（× 才断开）；attach 前的输出经 64KB 回放缓冲自动补发。断线遮罩下可重试。

### 刷新与重连

- 对话事件服务端按会话缓冲（约 2000 条/对话 tab），浏览器刷新/断线自动重连并重放（序号断档自动全量重排；服务重启后旧会话自动复活为空会话；断线期间顶部显示「正在重连」横幅）
- 确认/提问弹窗在等待期间断开浏览器 = 按拒绝/取消处理（不会卡死任务）；决议发送失败弹窗保持可重试；关闭带挂起弹窗的会话时提示「服务端按拒绝放行」

### 资源回收与健康探活

登录令牌 7 天过期（每小时清扫）；登录失败连续触发指数限速（冷却翻倍至 30s，429）；闲置 30 分钟无浏览器连接且非执行中的对话会话与闲置 1 小时无人观看的终端被服务端自动回收（重新打开即恢复）；`GET /api/health` 无鉴权探活（含审计写失败计数与当前分片，供监控告警）。

## Agent 能力

### Plugin 体系（10 插件 35 工具）

| Plugin | Tools | 说明 |
|--------|-------|------|
| ShellExecutor | `execute_command` | 通用 shell 命令执行，兜底能力（仅在其他专用工具不适用时使用） |
| FileManager | `read_file`, `write_file`, `list_directory`, `find_files`, `grep_files` | 文件读写、目录浏览、文件搜索与内容检索 |
| DockerManager | `docker_ps`, `docker_logs`, `docker_compose_up`, `docker_compose_down`, `docker_exec` | Docker/Compose 操作 |
| SystemdManager | `systemctl_status`, `systemctl_start`, `systemctl_stop`, `systemctl_restart`, `journalctl_logs` | Systemd 服务管理 |
| PackageManager | `package_install`, `package_remove`, `package_search`, `package_list` | 自动检测 apt/yum/dnf |
| DiagnosticKit | `check_port`, `check_process`, `check_disk`, `check_memory`, `check_network`, `tail_log`, `system_info` | 故障排查专用工具集 + 系统信息采集 |
| NetworkPlugin | `ping`, `dns_lookup`, `http_check` | 网络连通性检测（ping / DNS / HTTP） |
| KnowledgeBase | `query_consensus` | 查询当前环境的基础设施事实和运维规范（基于 Profile，控制面） |
| MemoryManager | `memory_read`, `memory_write`, `memory_delete` | 服务器记忆持久化（按主机分域，控制面） |
| ClarifyPlugin | `ask_user` | 结构化需求澄清（Agent 循环直接拦截处理，控制面） |

目标型工具（输入带 `hosts` 参数）由 Registry 解析引用后按主机 fan-out：单目标结果原样透传；多目标 `===== host =====` 分节聚合（总预算 10000 字符、每主机下限 500，截断保分节头），单主机 panic/失败降级为该主机 `[FAILED]` 分节。每个工具自声明风险等级（组装时灌入安全门，单一真相源）；模型可控字段（容器名/服务名/包名/路径）一律 argv 形式执行，参数不经 shell 重新解释。

### Skill 体系（斜杠命令）

第三种扩展点。与提供工具的 Plugin、注入静态事实的 Profile 共识不同，Skill **不新增工具**，而是在被斜杠命令触发时把一段专家 Playbook（过程性流程指南）注入对话，指导 Agent 组合现有工具完成一类任务，复用现有 ReAct 循环与安全门控。Skill 文件为 Markdown + YAML frontmatter，编译期 `go:embed` 嵌入二进制；未注册的斜杠输入原样交给 Agent 处理。

| Skill | 命令 | 说明 |
|-------|------|------|
| 服务器巡检 | `/inspect` | 系统性健康检查：资源水位（CPU/内存/磁盘/负载）、服务健康、网络连通、安全审计，对话输出 Markdown 汇总（别名 `/inspection`、`/healthcheck`） |
| 软件安装 | `/install` | 按发行版选包管理器（apt/yum/dnf/pacman/apk/zypper）、按环境适配镜像源与 HTTP 代理、异常处理、安装后验证并按需起服务（别名 `/inst`、`/pkg`） |

```bash
> /inspect          # Agent 逐项检查并汇总健康状态
> /install nginx    # 探测发行版与源 → 按需换源/走代理 → 确认后安装 → 验证 → 按需起服务
```

### Profile 环境共识

启动时按 `GENGAR_ENV` 加载对应 Profile（默认 `dev`，YAML 编译期嵌入）；Agent 系统提示词自动注入共识目录，经 `query_consensus(topic=<id>)` 查询具体条目（不凭直觉编造）。

- **基础设施事实**：YUM 源地址、NTP 服务器、HTTP 代理等
- **运维规范**：备份策略、服务命名约定等
- 可用 Profile：`dev`（默认）/ `prod`；Web 端共识加载失败降级继续服务，headless 端直接退出

### 服务器记忆（Memory）

每台受管主机一个 JSON 文件（`<data>/memory/<host>.json`），跨会话保持上下文、避免重复探测：

- `system_info`：操作系统、内核、CPU、内存、磁盘等基本信息
- `history`：Agent 执行过的关键操作（安装、配置变更等）

Agent 按系统提示词中的使用指南按需读写；文件不存在或损坏不影响启动。重命名主机视为新机器从零记忆。手动清空：`rm <data>/memory/<host>.json`。

### 流式输出与长会话鲁棒性

- **SSE 流式**：逐 token 推送到浏览器（思考指示器 + 每步耗时 + 轮尾统计），长输出不被整请求超时误杀（读空闲看门狗 90s，数据持续到达的慢流不误杀）
- **上下文压缩**：每步调 LLM 前检查（阈值 0.8 × 200K token），超限把最旧 tool_result 替换为占位符（保留最近 6 条完整；确定性替换、零额外 API 调用），模型可按占位符重跑被省略的命令
- **截断与取消不静默**：`stop_reason=max_tokens`（输出被上限掐断）会显式提示发「继续」补全；Esc 取消保留已生成的部分文本，并为已收集未执行的工具调用补写 tool_result——会话不报废，下一轮继续可用

## 安全模型

### 2 级安全分级

非黑即白，任何不被证明安全的操作都需要确认（默认拒绝）：

| Level | 值 | 含义 | 行为 |
|-------|----|------|------|
| Safe | 0 | 只读 / 安全 | 自动执行，不打扰用户 |
| Risky | 1 | 有副作用 / 不可证明安全 | 必须由用户确认后才执行 |

命令按 shell 运算符（`;` `|` `&&` `||`）拆段**逐段**判定：每段都命中白名单整串才免确认——`cat x; rm -rf /data` 这类「白名单命令夹带删除」的绕过形态一律弹确认；含命令替换（`$(...)`/反引号）或重定向（`>`/`>>`）直接确认。链式只读组合（`df -h; free -m`、`ps aux|grep nginx`）仍免确认。`curl`/`wget`/`env` 不在免确认名单内。黑名单正则感知复合命令与 `sudo`/`doas` 前缀（`ls && reboot` 中的危险部分被捕获）；引号内分隔符同样拆段——误拆只多弹确认，绝不放行。插件工具的风险分级以插件声明为单一真相源（组装时注入安全门）。

### 凭据加密与 SSH 主机密钥

- SSH 密码/私钥口令只在清单里存引用，本体由 `EncryptedStore` 以 **AES-256-GCM** 加密后存 SQLite `secrets` 表；**AAD 绑定引用**——密文行被复制到别的引用后认证标签不匹配，防错位重放
- 主密钥默认 `<data>/secret.key`（首次自动生成，0600，O_EXCL 原子创建）；容器部署可用 `GENGAR_MASTER_KEY`（hex 32 字节）或 `--master-key-file` 注入。**数据目录权限即安全边界**
- SSH 主机密钥 TOFU 首信策略：首连记录指纹（`<data>/known_hosts`），已记录则严格校验，不匹配拒绝连接（疑似中间人）

### 操作审计

每次工具调用（含用户拒绝、分发失败）落一条审计记录：会话、**操作者用户名**、工具、原始输入、实际生效主机、是否批准、结果。JSONL 按天分片（`audit-YYYY-MM-DD.jsonl`，保留 30 天，权限 0600），每条立即 fsync——崩溃/断电后仍完整；写失败计数暴露到 `/api/health` 作告警信号。

## 配置

### 命令行参数（gengar-server）

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--data` | `~/.gengar-pro/` | 数据目录（gengar.db/memory/audit/known_hosts/keys/secret.key） |
| `--listen` | `:8080` | 监听地址 |
| `--add-user <name>` | — | 创建登录用户后退出（密码读 `GENGAR_NEW_PASSWORD`，否则交互输入两遍） |
| `--master-key-file` | `<data>/secret.key` | 主密钥文件路径（`GENGAR_MASTER_KEY` 设定时优先） |

### 环境变量

| 环境变量 | 必填 | 默认值 | 说明 |
|---------|------|--------|------|
| `ANTHROPIC_API_KEY` | 否¹ | — | Anthropic API Key |
| `ANTHROPIC_BASE_URL` | 否 | `https://api.anthropic.com` | API 基础 URL |
| `ANTHROPIC_MODEL` | 否 | `claude-sonnet-4-20250514` | 使用的 Claude 模型 |
| `GENGAR_ENV` | 否 | `dev` | 环境共识 Profile（dev/prod） |
| `GENGAR_MASTER_KEY` | 否 | — | 凭据主密钥（hex 32 字节，优先于密钥文件；容器部署用） |
| `GENGAR_NEW_PASSWORD` | 否 | — | `--add-user` 的密码来源 |
| `GENGAR_SECRETS` | 否 | — | headless 内联秘密 `ref1=pass1,ref2=pass2`（值不能含逗号） |
| `GENGAR_COMMAND_TIMEOUT` | 否 | `30s` | 命令超时（headless 入口） |
| `GENGAR_KNOWN_HOSTS` | 否 | — | known_hosts 重定向（兜底；两入口恒用 `<data>/known_hosts`） |

¹ Web 端可改由设置页保存（api_key 入加密存储）或首次启动从 `~/.claude/settings.json` 自动导入。优先级链：

- **api_key**：环境变量 > 加密存储（设置页保存）> 旧 settings 明文（读出即迁入加密存储并清明文，幂等）> `~/.claude/settings.json` 一次性引导（字段级独立补齐、已有值永不覆盖、二次启动自然短路）
- **base_url / model**：SQLite settings > 环境变量 > 引导 > 默认值
- 设置端点仅 admin；保存后服务端重建全部在线用户的 Agent（其他用户对话收到「已重置」提示帧）

## headless 入口（自动化与测试）

`cmd/gengar-headless` 是无 Web 验证入口（测试设施）：stdin 逐行驱动 Agent 循环，plain Sink 字节稳定输出，斜杠命令与 Skill 全保留。

```bash
echo "帮我看看磁盘使用情况" | \
  go run ./cmd/gengar-headless --data ~/.gengar-pro --secrets ref=pass --pin-all --yes
```

`--yes` 自动批准全部 Risky 确认，`--pin-all` 把清单内全部主机设为默认目标，`--secrets` 内联注入秘密（不碰真实凭据存储）。适合脚本与 E2E 测试（`internal/e2e` 即以它为被测入口：桩 LLM + Docker sshd + YAML 用例，详见 `internal/e2e/README.md`）。

## 数据目录

`--data` 指定（默认 `~/.gengar-pro/`）：

| 路径 | 说明 |
|------|------|
| `gengar.db` | SQLite：主机清单 / 设置 / 用户 / 加密凭据（WAL 模式，单连接串行化） |
| `memory/` | 每主机记忆 JSON（`<host>.json`） |
| `audit-YYYY-MM-DD.jsonl` | 操作审计按天分片（保留 30 天，含操作者用户名；旧固定名 `audit.jsonl` 不再续写但保留） |
| `known_hosts` | SSH TOFU 主机密钥 |
| `keys/` | 上传的 SSH 私钥（按主机 ID 命名） |
| `secret.key` | 凭据主密钥（首跑自动生成 0600） |

## 项目结构

```
gengar-pro/
├── main.go                         # Web 服务入口（embed 前端 / 装配 EncryptedStore / --add-user / 内置 admin 种子 / 优雅退出）
├── main_test.go
├── frontend/                       # React + Vite 前端（构建产物 dist 由 go:embed 内嵌）
│   ├── src/
│   │   ├── App.tsx                 # 应用骨架（登录门 + 侧栏 + 对话/终端标签页、WS 事件接线、会话管理）
│   │   ├── main.tsx                # React 挂载入口
│   │   ├── api/
│   │   │   ├── client.ts           # REST 封装（函数对齐端点形状；401 统一切回登录页，登录透传文案）
│   │   │   ├── events.ts           # WS 订阅层（自动重连 + since 续传 + 断档全量重排 + 心跳 + 连接状态回调）
│   │   │   └── *.test.ts           # 传输层单测（401 分流、帧去重/重排）
│   │   ├── timing.ts               # 计时纯函数层（事件归约器 / 思考残留 / 工具耗时 / 轮尾统计）
│   │   ├── form.ts                 # 表单纯函数（标签解析）
│   │   ├── folders.ts              # 侧栏目录树纯函数（buildTree / allFolderPaths）
│   │   ├── types.ts                # 与 webapi JSON 形状一一对应的前端契约类型
│   │   ├── components/
│   │   │   ├── Login.tsx           # 登录页（服务端文案透传，含 429 限速）
│   │   │   ├── Sidebar.tsx         # 主机清单目录树（拖拽归类 / 右键菜单 / 终端与编辑入口 / 账户区）
│   │   │   ├── Chat.tsx            # 消息流渲染（memo 化，流式 delta 只重渲染末块）
│   │   │   ├── Markdown.tsx        # assistant 文本 Markdown 渲染（忽略原始 HTML，链接新标签）
│   │   │   ├── InputBar.tsx        # 输入区（Enter 提交 / Esc 取消）
│   │   │   ├── ScopePicker.tsx     # 对话目标选择器（按目录分组复选，一经选定不可改）
│   │   │   ├── ConfirmModal.tsx    # 批量确认弹窗（高风险严格模式）
│   │   │   ├── AskModal.tsx        # ask_user 提问弹窗
│   │   │   ├── TerminalPane.tsx    # SSH 终端（xterm.js；懒加载，首包不含 xterm）
│   │   │   ├── HostForm.tsx        # 主机录入/编辑表单（私钥上传 / 表单内测试）
│   │   │   ├── ImportDialog.tsx    # XShell 上传导入（预览判重 / 批量提交；懒加载）
│   │   │   ├── Settings.tsx        # 设置弹窗（掩码回显；懒加载）
│   │   │   ├── UsersDialog.tsx     # 用户管理弹窗（仅 admin；懒加载）
│   │   │   └── ErrorBoundary.tsx   # 渲染异常降级卡片（对话区/终端分别包裹）
│   │   ├── assets/                 # 耿鬼 logo
│   │   ├── styles.css              # 「耿鬼紫夜」主题样式
│   │   └── vite-env.d.ts
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts              # dev 模式 /api 代理（含 WS）到 127.0.0.1:8080
├── cmd/
│   └── gengar-headless/main.go     # 无 Web 验证入口（stdin 驱动 Agent 循环，测试设施）
├── internal/
│   ├── webapi/                     # Web 服务层（登录会话 + REST + WS，向下调 appcore）
│   │   ├── server.go               # 路由装配 + 后台 reaper（token 清扫/闲置会话与 PTY 回收）+ /api/health
│   │   ├── auth.go                 # 登录/登出/me（bcrypt + cookie 令牌 TTL 7 天 + 指数限速）
│   │   ├── users.go                # 用户管理 REST（仅 admin；重置/删除即时吊销令牌，删除移交主机）
│   │   ├── hosts.go                # 主机管理 REST（按用户视角隔离；增删改/测试/私钥上传/目标 scope）
│   │   ├── settings.go             # 全局设置 REST（团队共享一份，掩码回显，保存后重建 Agent）
│   │   ├── state.go                # chatSession 会话模型（每对话 tab 一个：目标/hub/sink/Agent/任务锁）
│   │   ├── chat.go                 # 对话端点（提交/取消/确认与提问决议回填）+ 对话 WS 事件流
│   │   ├── hub.go                  # 每会话事件枢纽（2000 帧有界 FIFO + 序号重连重放 + fan-out）
│   │   ├── sink.go                 # ui.Sink 的 web 实现（确认/提问阻塞等 REST 决议；断连按拒绝放行）
│   │   ├── termws.go               # 终端端点（REST 开/关 + WS 双向流，多端附加同一 PTY）
│   │   ├── xshell.go               # XShell 上传导入（解析预览 → 勾选提交）
│   │   ├── json.go / static.go     # JSON 读写小工具 / 前端静态托管（SPA fallback + immutable 缓存）
│   │   └── *_test.go               # 各端点单测 + isolation_test.go（用户隔离端到端）
│   ├── agent/                      # ReAct 循环核心
│   │   ├── agent.go                # 主循环（SSE 流式消费 / 批量确认 / 审计 / ask_user / 取消补写 tool_result）
│   │   ├── conversation.go         # 会话上下文管理
│   │   └── token.go                # Token 估算与上下文压缩（0.8×200K 阈值，保留最近 6 条 tool_result）
│   ├── appcore/
│   │   ├── core.go                 # 组装层（清单+执行+钉住+目标解析+BuildAgent，headless 与 Web 共用）
│   │   └── session.go              # Session 单用户视图（钉住/清单按用户视角隔离，webapi 每登录用户一个）
│   ├── audit/
│   │   └── audit.go                # 操作审计流水（JSONL 按天分片轮转，每条 fsync，含 user；写失败计数入 health）
│   ├── host/
│   │   ├── host.go                 # Host / AuthConfig 数据模型（含 Folder 目录、Owner 属主字段）
│   │   ├── folder.go               # 目录路径规范化（NormalizeFolder）
│   │   ├── resolve.go              # 目标引用解析（主机名 / 标签 / all）
│   │   ├── store.go                # SQLite 清单 / 设置 KV / 用户 / 密文（幂等 ALTER 迁移；ListFor 视角查询）
│   │   ├── secrets.go              # SecretStore 接口（内存实现 / 环境变量解析，测试与 headless 用）
│   │   └── encrypted.go            # EncryptedStore（AES-256-GCM，AAD 绑定 ref）+ 主密钥加载
│   ├── executor/
│   │   ├── executor.go             # Executor 接口 + Local 实现
│   │   ├── ssh.go                  # SSH 拨号（全握手 deadline）与执行器（session 执行 / SFTP / argv 单引号引用）
│   │   ├── knownhosts.go           # TOFU 主机密钥校验
│   │   └── manager.go              # 按主机缓存 SSH 连接（探活看门狗 + 死连接重拨）
│   ├── term/
│   │   └── term.go                 # 内嵌交互终端：按「主机+属主」专属 PTY（64KB 回放缓冲 / 写入互斥 / 并发单拨号）
│   ├── xshell/
│   │   └── xshell.go               # XShell .xsh 解析（UTF-16LE INI / 文件夹→目录与标签 / 容错 / 不读密码）
│   ├── config/
│   │   ├── config.go               # 全局配置（API Key/BaseURL/模型/超时），环境变量加载
│   │   └── claude.go               # 解析 ~/.claude/settings.json 的 env 对象（启动引导用）
│   ├── command/
│   │   └── command.go              # 斜杠命令 Registry（名称/别名/前缀匹配；Skill 命令注入此处）
│   ├── claude/
│   │   ├── client.go               # HTTP 客户端（非流式 + 指数退避重试 + 输出截断）
│   │   ├── stream.go               # SSE 流式客户端（tool_use 碎片累积 / 读空闲看门狗）
│   │   └── types.go                # Claude API 类型定义
│   ├── safety/
│   │   ├── level.go                # 2 级安全等级（Safe/Risky）
│   │   ├── gate.go                 # 动态命令分析引擎（分段判定）
│   │   └── rules.go                # 内置安全规则（黑白名单/受保护路径）
│   ├── skill/
│   │   ├── skill.go                # Skill 类型、frontmatter 解析、加载、ToCommand、Prompt
│   │   └── data/                   # inspect.md / install.md（go:embed）
│   ├── plugin/
│   │   ├── types.go                # Plugin 接口与 ToolDefinition（Targeting 标记）/ ToolResult（Hosts）
│   │   ├── registry.go             # Plugin 注册中心（目标解析 + 按主机 fan-out 与聚合 + 分级单一真相源）
│   │   ├── shell.go / file.go / docker.go / systemd.go / pkgmgr.go
│   │   ├── diagnostic.go / network.go
│   │   └── knowledge.go / memory.go / clarify.go
│   ├── memory/
│   │   └── memory.go               # 按主机分域的记忆存储（memory/<host>.json，路径清洗防穿越）
│   ├── consensus/
│   │   ├── consensus.go            # 环境共识加载与查询
│   │   └── data/                   # Profile YAML（dev/prod，嵌入二进制）
│   ├── observability/
│   │   └── e2elog.go               # 结构化事件日志（JSONL；Noop/内存/文件三实现）
│   ├── ui/
│   │   ├── sink.go                 # Sink 接口（ConfirmBatch）/ ConfirmRequest（Hosts/HighRisk）/ 自动批准开关
│   │   ├── truncate.go / spinner.go / writer.go   # 确认摘要截断 / 工具展示名 / 行缓冲适配器
│   │   └── plain/                  # 非 TTY 输出（headless / 测试，字节级稳定）
│   ├── integration/                # Docker sshd 集成测试（go:build integration 隔离）
│   └── e2e/                        # E2E 测试框架（go:build e2e 隔离：桩 LLM + Docker sshd + headless 驱动）
│       ├── e2e_test.go / cases.go / harness.go / sshd.go / stubllm.go
│       ├── README.md               # E2E 运行说明
│       └── testcases/              # YAML 测试用例（E01-E07 / R01）
├── build/                          # 构建产物输出目录
├── go.mod / go.sum
└── .psy/                           # psyduck 设计文档归档（按日期）
```

## 运行测试

```bash
go test ./...                        # 单元测试（不加 -race：本机工具链 -race 损坏，并发正确性靠压力用例补偿）
cd frontend && npm test              # 前端单测（vitest，纯函数与传输层）
go test -tags integration ./internal/integration/   # Docker sshd 集成测试（Docker 不可用自动 Skip）
# E2E 详见 internal/e2e/README.md（桩 LLM 确定性回放 + 真机冒烟 R01）
```

## 技术栈

- **后端**：Go 1.26+（标准库 net/http 方法路由）；`golang.org/x/crypto`（ssh/bcrypt/AES-GCM）、`github.com/gorilla/websocket`、`modernc.org/sqlite`（纯 Go 驱动，无 CGO）、`github.com/pkg/sftp`、`gopkg.in/yaml.v3`
- **LLM**：Claude Messages API（HTTP + SSE streaming，零 SDK 依赖）
- **前端**：React 18 + TypeScript + Vite + xterm.js + react-markdown（构建产物 go:embed 内嵌；终端与低频弹窗懒加载）

## 已知限制（alpha）

- 会话不持久化：服务重启即新会话（浏览器刷新不受影响，闲置会话被回收后重开即恢复）
- 设置变更会重置全部用户对话（有提示帧）
- 交互式 `--add-user` 输入有回显（在服务器本地控制台执行）
- 重置密码/删除用户后，已建立的 WS 连接保持到自然断开（新请求已 401）
- 集成测试与 E2E 需 Docker；本机 `-race` 不可用（工具链问题，见「运行测试」）

## 人工验收清单（Alpha 收尾自查）

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

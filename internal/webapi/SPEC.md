---
psy_kind: factual
psy_version: 1
package: internal/webapi
---

# internal/webapi

## 概述

`internal/webapi` 是 gengar-pro 的 Web 服务层：用户名/密码登录（bcrypt + cookie 会话令牌，TTL 7 天 + 登录失败指数限速）、REST API（主机/设置/钉住/对话/终端/XShell 导入/用户管理/健康检查）与 WS 事件流（对话事件 + 交互终端），向下调 appcore。单实例进程内状态（token 表、每对话 tab 一个 chatSession：目标/hub/Agent/任务锁按会话隔离；后台 reaper 回收闲置会话与 PTY），不依赖外部服务；前端产物经 `StaticHandler` 同端口托管（hashed 资产 immutable 缓存）。

## 文件

- `server.go` — `Server` 组装：路由表、设置保存后全量重建 Agent（含 hub reset 广播）、`StartBackground`（reaper：token 过期清扫/闲置 chatSession 回收/闲置 PTY 关闭）、`GET /api/health`、`SetTermManager`/`CloseTerminals`/`OnTermEvent`、命令注册表（Skill）
- `auth.go` — 登录/登出/me：bcrypt 校验 + 32 字节随机 cookie 令牌（tokenEntry 含签发时间，TTL 7 天惰性过期 + 每小时清扫；cookie MaxAge 对齐）；dummy-hash 均衡两条失败路径耗时；登录失败指数限速（同一用户名冷却翻倍至 30s，成功清零）+ bcrypt 并发闸；login/me 响应带 role
- `hosts.go` — 主机管理 REST（全部按用户视角隔离——设计 `.psy/2026-10-30/2026-10-30-user-isolation-design.md`）：列表（`ListFor` 视角；admin 全见）/保存（新建归属操作者、编辑保留原归属；编辑/防复活经 `findHostFor` 属主过滤，他人主机等价不存在）/删除/连通测试/草稿测试/终端开启（属主校验前置）/对话目标 scope（`PUT /api/chat/scope`，过滤「视角内」主机+会话流回显+`scopeNames` 按会话创建者视角）；编辑保留原引用与 KeyPath、防复活复查、私钥上传落盘 `keys/<hostID>.key` + 解析验证、KeyPath 白名单——客户端声明的 keyPath 只接受 keysDir 内路径否则 400「私钥请上传」；删除顺带清秘密、key 文件与在线终端；表级 UNIQUE 重名转 400 文案；`badReqError` 业务错误→400 分流
- `settings.go` — 全局设置 REST（团队共享一份）：掩码回显、`sk-****` 前缀视为未修改，保存后锁外重建全部在线用户的 Agent
- `state.go` — 会话模型：`chatSession`（自持 appcore.Session=对话目标 + hub/sink/Agent/单任务锁，每对话 tab 一个；owner 属主 + viewAdmin 视角快照——`NewSessionFor` 按用户视角建 Session，Agent 目标解析/目录只含自己主机；请求路径一律经 `chatForUser` 属主过滤，未知与他人会话统一 nil/400 防存在性探测；WS hello 绑定 id 并通告 scope，stale 复活，attach 前置属主校验 403）+ `chatFor`/`chatForUser`/`chatOpen`
- `chat.go` — 对话端点（全部带 `session` 路由到对应 chatSession，未知 400）：提交（任务 goroutine，Background ctx 长于 HTTP 请求）/取消（ctx cancel + 放行待决议）/确认与提问决议回填 + 对话 WS（session 参数空/未知即复活新会话；hello 带 session→重放→实时推送，读循环 90s 空闲掐断）
- `hub.go` — 每会话事件枢纽：有界 FIFO 缓冲（2000 帧，序号单调）+ 活跃订阅者 fan-out（锁内非阻塞投递保证按序号有序）；`onIdle` 订阅数 1→0 回调；`reset()`（清缓冲 + 发 reset 帧，设置变更重建 Agent 后调用）；`hasSubs()`（reaper 判定基准）
- `sink.go` — `userSink`（`ui.Sink` 的 web 实现）：事件推 hub；ConfirmBatch/AskUser 阻塞等 REST 决议（锁内原子认领，只决议一次）；断连按拒绝/取消放行
- `termws.go` — 终端端点：REST 开（按主机 GetOrCreate，属主校验前置——`findHostFor` 视角过滤，他人主机 404）/关 + WS 双向流（attach 先补发回放缓冲——open 与 attach 之间的输出不丢）；`termHub` 按 sessionID fan-out（多端附加同一 PTY）+ lastSeen 记录（reaper 据此回收无订阅 1 小时的 PTY）；`termConn` 接口（含 Replay，测试注入假实现）
- `xshell.go` — XShell 上传导入：base64 文件列表 → `xshell.ParseFiles` 解析预览（判重 + 重名后缀，判重集合按导入者视角）→ 勾选提交（复用 saveHost 语义，归属导入者；首错中止）
- `users.go` — 用户管理端点（仅 admin，`admined` 包裹 authed 再验角色）：列表/创建（固定普通角色）/重置密码/删除；删除先把名下主机移交 admin（`ReassignHosts`，失败中止可重试）；重置与删除即时吊销目标用户 token；删除顺带摘除其 appcore.Session 与全部对话会话（取消在跑任务）；操作记审计。`viewerOf` 视角辅助（username + isAdmin，各主机端点共用）
- `json.go` — `writeJSON`/`writeErr`/`readJSON`（1MB 请求上限）
- `static.go` — `StaticHandler`：fs.FS 构建带 SPA fallback 的静态托管（未命中/目录落 index.html，防目录列表泄露）；hashed assets 设 `Cache-Control: immutable`（一年），HTML `no-cache`
- `auth_test.go` / `hosts_test.go`（含设置端点）/ `users_test.go` / `chat_test.go` / `hub_test.go` / `termws_test.go` / `xshell_test.go` / `static_test.go` / `isolation_test.go`（主机按用户隔离端到端：列表/写操作/终端/导入判重/删除移交/scope） — 各端点单测（httptest + 假 termConn/fake core，无需真实 SSH）

## API

- `New(core *appcore.Core, auditLog *audit.Logger, static http.Handler) *Server` — 组装路由；static 为 nil 时不挂前端（单测场景）；consensus 加载失败降级 nil 继续服务（区别于 headless 直接退出：Web 是长驻多用户进程）
- `(s *Server) ServeHTTP(w, r)` — 直接作为 http.Handler
- `(s *Server) SetTermManager(m termConn)` — 注入终端管理器（入口创建 `term.NewManager(secrets, s.OnTermEvent)` 后调用；nil 表示未启用，终端端点报 503）
- `(s *Server) SetKeysDir(dir string)` — 注入私钥上传目录（入口设 `<data>/keys`；未设时上传私钥报错）
- `(s *Server) CloseTerminals()` — 进程退出前关闭全部 PTY
- `(s *Server) StartBackground() func()` — 启动后台清扫 goroutine（1 分钟 tick：每小时 token 过期清扫；每分钟 reapIdleSessions——无订阅 30 分钟且非 busy 的 chatSession 回收；termHub.reapIdle——无 WS 订阅 1 小时的 PTY 关闭）。返回停止函数（main 退出时调用）
- `(s *Server) OnTermEvent(ev term.Event)` — 暴露给入口做 term.Manager 的 Emitter
- `StaticHandler(fsys fs.FS) http.Handler` — 前端产物托管（embed 声明在模块根 main.go，此处只收 fs.FS）

### REST 端点

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/api/login` | 用户名+密码登录，Set-Cookie（HttpOnly/SameSite=Lax/MaxAge=7天），响应带 role；失败连续触发指数冷却（429） |
| POST | `/api/logout` | 删除令牌并清 cookie |
| GET | `/api/me` | 探测登录态（前端启动时调用），响应带 role |
| GET | `/api/health` | 无鉴权探活：ok/time/sessions + 审计写失败计数与当前分片（运维告警信号） |
| GET/POST | `/api/hosts`、`/api/hosts` | 列表（含连通状态；**按用户视角**——普通用户只见自己名下的，admin 全见）/ 保存（新增归属操作者或编辑——他人主机等价不存在） |
| DELETE | `/api/hosts/{id}` | 删除（顺带清秘密引用、关在线终端；非属主且非 admin → 404） |
| POST | `/api/hosts/{id}/test` | 拨号 + echo 探测，成功采集 OS 回写（属主校验同上） |
| POST | `/api/hosts/test` | 表单内测试：body 同 hostReq，未保存参数一次性拨号（不落库/不落盘；req.ID 非空才回写侧栏状态；req.ID 指他人主机 → 404） |
| GET/PUT | `/api/settings`、`/api/settings` | 读（API Key 掩码回显）/ 写（成功后重建全部 Agent + 广播 reset 帧）；仅 admin |
| PUT | `/api/chat/scope` | 设/改该会话对话目标 `{session, hostIds}`（过滤为该用户视角内的主机；会话流回显 🎯） |
| POST | `/api/chat` | 提交对话（body 带 `session`，按会话单任务锁；斜杠命令走命令注册表）；任务 goroutine 异步执行 |
| POST | `/api/chat/cancel` | 取消任务（body 带 `session`；ctx cancel + CancelPendings） |
| POST | `/api/confirm/{id}` | 确认决议回填 `{approved, session}`（ok=false 时前端弹回弹窗重试） |
| POST | `/api/ask/{id}` | 提问决议回填 `{answer, session}` |
| POST | `/api/hosts/{id}/term` | 开/附加终端会话（按主机 GetOrCreate，**属主校验前置**：他人主机 404），返回会话 ID |
| POST | `/api/term/{id}/close` | 显式关闭会话（幂等；WS 断开不触发关闭） |
| POST | `/api/xshell/import` | 上传解析预览 `{files:[{name,data(base64)}]}`（判重集合按导入者视角） |
| POST | `/api/xshell/commit` | 勾选批量入库 `{hosts:[hostReq]}`（归属导入者） |
| GET | `/api/users` | 用户列表（仅 admin；不回显哈希） |
| POST | `/api/users` | 创建普通用户 `{username,password}`（仅 admin；重名 400） |
| PUT | `/api/users/{username}/password` | 重置密码（仅 admin；成功吊销该用户全部 token） |
| DELETE | `/api/users/{username}` | 删除用户（仅 admin；不能删自己/最后一个 admin；**名下主机移交 admin**；吊销 token + 摘除状态） |

`/api/` 未命中统一 404（不落 SPA fallback）；`/` 挂 static 时为前端 SPA。除 login/health 外全部经 `authed` 包裹（cookie 校验 + TTL，401「未登录」）；设置与用户管理另经 `admined`。

### WS 端点与帧协议

**`GET /api/ws/chat?since=<seq>&session=<id>`**（cookie 登录，upgrader 同源校验；session 空/未知即复活为新会话）下行帧 `wsFrame{t, seq, next, session, ev, id, reqs, input}`：

- `t: hello` — 连接即发，`session` 为服务端分配的会话 ID（与请求不符即重绑：客户端迁移状态）、`scope` 为该会话目标主机名（顿号连接，空=未设定）、`next` 为当前最新序号（服务重启序号回退时客户端据此触发全量重排）
- `t: chat` — 对话事件（`ev` 与前端 ChatEvent 对应：thinking/delta/tool_start/tool_result/text_end/step/error|warning|info/done）
- `t: confirm` / `t: ask` — 弹窗请求（`id` + `reqs`/`input`；入缓冲，刷新重放会重新弹出）
- `t: confirm_done` / `t: ask_done` — 决议终态（重放时先弹后收，终态正确）
- `t: reset` — 客户端序号与缓冲错位（since 早于缓冲头 / 服务重启序号清零），清状态后全量重建

重连带 `since` 续传；上行帧忽略（客户端 30s 心跳即刷新源）。服务端 90s 无上行判停滞掐断、单帧写超时 10s。

**`GET /api/ws/term?id=<sessionID>`**（attach 前会话必须已存在——先经 REST open）：

- 下行 `termFrame{t: data|exit}`（data 为 base64 原样；exit 可带 error）；attach 时先补发回放缓冲（最近 64KB，补发先于 subscribe 保证顺序）
- 上行 `termFrame{t: write|resize}`（close 走 REST，不经 WS；ping 等其他帧只刷新读 deadline）
- 无序号（断档靠新输出推进）；回放缓冲兜底 attach 前的输出；空闲读 deadline 5 分钟，浏览器 ping 保活 2 分钟

## 依赖

**内部包：** `agent`（AuditLogger）、`appcore`（Core/Session/LoadConfig）、`audit`、`command`、`config`、`consensus`、`host`、`skill`、`term`（Event；termConn 接口不直接依赖 *term.Manager）、`ui`

**第三方库：** `github.com/gorilla/websocket`、`golang.org/x/crypto/bcrypt`

**标准库：** `context`、`crypto/rand`、`encoding/base64`、`encoding/hex`、`encoding/json`、`errors`、`fmt`、`io/fs`、`net/http`、`net/url`、`os`、`strconv`、`strings`、`sync`、`time`

## 设计重点

### 每用户 hub：缓冲、序号与断连取消

每会话（对话 tab）一个 `hub`：帧发布即分配单调序号并写入 2000 帧有界 FIFO（慢消费者非阻塞丢帧，客户端按序号断档检测后全量重连）；订阅者 fan-out 在锁内进行——锁序化并发 publish 保证每订阅者按序号收帧。WS 连接顺序恒为 subscribe → snapshot：窗口内新帧既进通道又进缓冲，客户端按 `seq ≤ lastSeen` 去重；snapshot 先于 subscribe 会漏帧，不可调换。浏览器全关（订阅数 1→0）触发 `onIdle` → `CancelPendings`：阻塞在确认/提问的任务按拒绝/空答案放行，单任务锁与 Agent goroutine 不会永久卡住。

### termConn 接口注入

`termws.go` 自定义 `termConn` 接口（Open/FindByHost/Exists/Write/Resize/Close/CloseHost/CloseAll/Replay），`*term.Manager` 天然满足，单测注入假实现脱离真实 SSH。终端事件与请求路径解耦：单一 Emitter（`OnTermEvent`）经 `termHub` 按 sessionID fan-out 给全部订阅者（多端附加同一 PTY）。

### 生命周期与回收（reaper）

`StartBackground` 以 1 分钟 tick 维护三类资源：token 过期（惰性判定 + 每小时清扫）；闲置 chatSession（无 hub 订阅 30 分钟且非 busy——回收后前端重连即复活为新会话，与「服务重启会话清空」同语义）；闲置 PTY（termHub lastSeen 超 1 小时无订阅——关闭后前端 attach 收到「会话不存在」提示重开）。busy 会话永不回收（任务在跑）。

### 锁序：statesMu → cfgMu → agentMu，不嵌套

`chatOpen` 在 chatsMu 内绝不拿 cfgMu/agentMu（新建会话先释放 chatsMu 再 `rebuildAgent`，Agent 经 `sessFor(owner)` 绑定账号级钉住目标）；`rebuildAgent` 先在 cfgMu 内快照配置、释放后再拿 agentMu。任何路径不得同时持有 chatsMu 与 cfgMu——设置保存（`rebuildAllAgents`）也是锁外快照 chats 后逐个重建。任务执行期只拿 agentMu 取指针、锁外跑 ProcessInput。

### 按主机 GetOrCreate 终端

`handleTermOpen` 先 `FindByHost` 命中即复用（多端共享 PTY），未命中才 Open——term.Manager 内部的 per-host 互斥已保证并发 open 收敛为单拨号单会话，REST 层无需自带原子性。

### 用户管理与会话吊销

仅 admin 可管理用户：`admined` 在 `authed` 拿到用户名后每请求查一次 users 表验角色（角色创建后不可变，内网流量下省掉 token 表结构复杂化）。重置密码/删除用户即时吊销目标用户全部 token（强制重登录）；删除先把名下主机移交 admin（`ReassignHosts`，失败 500 中止可重试），再摘除其 appcore.Session 与全部对话会话并 cancel 在跑任务（锁序：chatsMu/userMu 内取走 → 释放 → taskMu）。已知限制（alpha）：已建立的 WS 连接保持到自然断开。内置 admin 由入口 `ensureAdmin` 种子（见 main.go），Web 创建的用户一律普通角色。

### 主机按用户隔离（视角模型）

`hosts.owner` 列（空 = admin 归属）是隔离的唯一事实源：`viewerOf(username)` 每请求查一次库得到视角（owner + isAdmin），主机各端点经 `ListFor`/`findHostFor` 过滤——普通用户只见/只解析自己名下的主机，admin 全见可操作。属主校验失败统一 404「主机不存在」（编辑路径沿用 400「主机已被删除」文案），与 `chatForUser` 同语义防存在性探测。chatSession 在创建时快照视角（`viewAdmin`）并经 `NewSessionFor` 建按视角的 appcore.Session——Agent 的 CatalogText/Names/Resolve 只含该用户的主机。表级 UNIQUE 的主机名冲突转 400 可操作文案（视角内判重后缀之外的最后防线）。

# gengar-pro Alpha 增强设计：主机编辑 · 对话流计时反馈 · 内嵌 SSH 终端

- 日期：2026-10-21
- 状态：已评审通过（三项关键决策经用户确认，线框图对照过 Visual Companion）
- 前置：`.psy/2026-10-21/2026-10-20-gengar-pro-desktop-design.md`（Alpha 垂直切片）

## 1. 背景

Alpha 交付后的三项用户反馈：

1. **主机只能录入不能编辑**——改个地址/标签必须删了重录，密码还得重填。
2. **对话框互动不流畅**——等待模型输出与工具执行期间界面零反馈，无法判断 Agent 是否卡死，每步耗时全凭猜。原 gengar CLI 有 Thinking 旋转指示器 + 秒数计数，GUI 没有对齐。
3. **只有 Agent 对话不够**——日常还需要亲手敲命令。希望 gengar-pro 本身就是运维工作台：除了对话框，能直接对主机开交互式终端。

## 2. 决策记录

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| E1 | 需求 3 范围 | **内嵌交互终端**（应用即工作台，主机仍直连） | ProxyJump 链路（部分主机需经跳板转发）→ 后续版本，本期不做 |
| E2 | 「Thinking」含义 | **等待指示器**（同原 CLI：旋转帧 + 动词 + 秒数） | extended thinking 流式展示模型推理文本 → 需改 claude client 与请求参数，token/延迟成本高，后续可选 |
| E3 | 计时实现 | **前端打点**（事件到达时刻 `performance.now()`，`setInterval` 驱动跳动） | 后端事件带时间戳：需改 gui sink 全部 emit 点；事件桥延迟毫秒级无感，且窗口后台节流只影响跳动显示、不影响起止差值准确性 |
| E4 | 终端布局 | **主区标签页**（对话 tab 常驻 + 每终端一 tab） | 底部分屏抽屉（一次一个、实现 resizer 复杂）；弹窗（遮挡对话流，长会话体验差） |
| E5 | 终端连接 | **独立 `internal/term` 包，专属 `*ssh.Client`** | 复用 Executor Manager 缓存连接：alive 探测/驱逐语义会砍掉长挂的终端会话 |
| E6 | 终端数据通道 | 下行 `term:data` 事件（Go 侧 base64 编码）；上行绑定方法 | 前端直连 ssh（wasm）：认证材料不能下发前端，安全不可接受 |
| E7 | 终端 × 单任务锁 | **互不阻塞**（各自独立 SSH 连接） | 共享全局锁：Agent 跑长任务时手敲终端被锁死，违背工作台定位 |
| E8 | 终端审计 | open/close 写 `audit.jsonl`，**不记内容** | 全量记录击键：单用户桌面工具，隐私与体积成本大于收益 |

## 3. 主机编辑（前端补口，后端零改动）

后端 `SaveHost` 的编辑路径已存在且有测试：非空 ID 走编辑，保留钥匙串引用与探测 OS，拒绝复活已删条目（`app.go` SaveHost）。缺口全在前端：

- Sidebar 主机行加 **✎ 按钮** → 以编辑模式打开 `HostForm`
- `HostForm` 编辑模式：预填名称/地址/端口/用户/标签（数组→逗号串）/角色/认证方式/密钥路径；提交带 `id`
- 密码/口令字段**留空 = 保持不变**（placeholder 注明「留空保持不变」），填写才覆盖钥匙串
- 弹窗标题随模式：「添加主机」/「编辑主机」
- 保存成功后 fire-and-forget `TestHost`，toast 反馈（与添加一致）
- busy 时禁用 ✎ 入口（与添加/设置按钮一致的保守策略）

## 4. 对话流 Thinking + 每步计时（纯前端，事件流不变）

Agent 循环已发出全部所需事件且经 gui sink 到达前端：`thinking`（每次 API 调用前 start / 流结束 stop）、`delta`、`tool_start`、`tool_result`、`done`。与原 gengar CLI 的 Sink 语义一致（已核对 `internal/agent/agent.go` 调用点）。

### 4.1 UI 元素（对照线框图）

- **思考指示器**（进行中）：底部 `⠋ Brewing… (2s)`。旋转帧 10 帧 braille、动词每次 `thinking:true` 随机取（Thinking / Pondering / Reasoning / Brewing / Cogitating / Musing / Deliberating / Reflecting，同原 CLI 词表）；首个可见输出（`delta` / `tool_start` / `text_end` / `error` / `done`）到达即结束。独立于消息流的临时 UI 态，非消息块。
- **思考残留行**（完成后）：`✻ 思考 12.4s` 暗色小字，作为 `thinking` 块插入当前 assistant 消息。**< 1s 不生成**（快速思考是噪音）。
- **工具耗时**：`tool_start` 打点；运行中黄色跳动 `5s…`（spinner 帧 + 秒数），`tool_result` 定格 `· 8.2s` 追加在工具行尾。**≥ 1s 才显示**。
- **轮尾统计**：`done` 到达时汇总当前轮（最近一条 user 消息之后）所有块：`── 本轮完成 · 共 41.6s（思考 15.5s · 工具 26.1s）──`，按 system 消息样式居中渲染。

### 4.2 前端数据模型（types.ts）

- `ToolBlock` 增加 `startedAt?: number`（`performance.now()`）、`durationMs?: number`
- 新增 `ThinkingBlock { kind: "thinking"; durationMs: number }`，`Block` 联合类型扩一项
- Reducer 新增事件处理：`thinking:true` 记 `thinkingStartAt` 并选动词；首个可见输出事件计算时长、≥1s 生成 `ThinkingBlock`；`tool_start` 打 `startedAt`；`tool_result` 算 `durationMs`；`done` 生成轮尾统计
- 计时跳动：**活动计时组件各自持 `setInterval`（100ms）本地重渲染**（思考指示器一个、每个运行中工具行一个；挂载起、完成/卸载清），不重渲染整个消息列表；对齐原 CLI「有活动指示才 tick」的自停思路

### 4.3 边界

- 窗口后台/最小化：WebView 节流 `setInterval` → 跳动暂停，起止差值仍准确；回前台即刻刷新
- 取消（Esc → ctx cancel）：事件流以 `done` 收尾，残留计时按已到达事件定格，不产生悬挂指针

## 5. 内嵌 SSH 终端（标签页）

### 5.1 Go 侧：`internal/term` 新包

```go
type Manager struct { /* sessions map[string]*Session；nextID */ }
func NewManager(secrets host.SecretStore) *Manager

// Open 拨号（专属 *ssh.Client，非 Executor Manager 缓存）、RequestPty(xterm-256color,
// 初始 80x24) + Shell，启动输出泵 goroutine；返回会话 ID。
func (m *Manager) Open(h *host.Host) (string, error)
func (m *Manager) Write(id, data string) error            // 前端 xterm onData 字符串直接写 stdin
func (m *Manager) Resize(id string, cols, rows int) error  // window-change
func (m *Manager) Close(id string)                         // 幂等；关 session + client
func (m *Manager) CloseAll()                               // app.Shutdown 调用
```

- **拨号复用**：把 `internal/executor/ssh.go` 的私有 `dialClient`（认证/known_hosts/超时逻辑）提取为导出 `DialClient`，executor 与 term 共用，认证行为一处维护
- **下行**：stdout/stderr 合并读取，每段 base64 编码后 `EventsEmit("term:data", {id, data})`——终端输出含任意二进制转义序列，显式 base64 保证 JSON 通道安全；进程退出推 `term:exit {id}`，错误推 `term:exit {id, error}`
- **生命周期**：会话随用户关 tab、DeleteHost 顺带关闭、应用 Shutdown 关闭而结束；内存态，无持久化

### 5.2 app.go 新绑定

- `OpenTerminal(hostID string) (string, error)`：查主机 → `term.Open` → 审计 `term_open`；返回会话 ID（前端建 tab）
- `TerminalWrite(id, data string) error` / `TerminalResize(id string, cols, rows int) error` / `TerminalClose(id)`（审计 `term_close`）
- `DeleteHost` 增补：关闭该主机的在线终端

### 5.3 前端

- 依赖：`@xterm/xterm` + `@xterm/addon-fit`（不引入其余 addon，YAGNI）
- `TerminalPane.tsx`：挂载 `Terminal` 实例；`onData` → `TerminalWrite`；addon-fit 的 resize → `TerminalResize`；订阅 `term:data`（base64 → `term.write`）与 `term:exit`（显示「连接已断开」，停写）
- **Tab 集成**（App.tsx）：主区顶部 tab 栏——💬 对话（常驻）+ 每终端一 tab（`>_ 主机名 ×`）；`activeTab` 状态；Sidebar 主机行加 `>_` 按钮，重复点击聚焦已有 tab；关 tab = `TerminalClose`
- **键盘语义**：终端 tab 激活时输入焦点在 xterm，Esc 归终端（仅对话 tab 激活时 Esc 触发 CancelTask）
- 拨号失败：toast 报错，不建 tab

## 6. 错误处理

| 场景 | 行为 |
|---|---|
| 终端拨号失败 | `OpenTerminal` 返回错误 → toast（含认证失败原因），不建 tab |
| 终端中途断线 | tab 内显示「连接已断开」+ `term:exit`，停写；不自动重连（alpha） |
| 主机被删时终端在线 | `DeleteHost` 顺带 `Close`，tab 收 `term:exit` |
| `TerminalWrite/Resize` 对已断会话 | `term:data` 已停；绑定方法返回错误被前端吞掉（tab 已显示断开） |
| 窗口关闭/应用退出 | `Shutdown → CloseAll`，SSH 会话随进程结束 |
| 窗口后台节流 | 计时跳动暂停，差值仍准（§4.3） |
| busy 时编辑主机 | 入口禁用（§3） |

## 7. 测试策略

- **Go（`internal/term`）**：复用 `internal/executor/testserver_test.go` 的内存 sshd——PTY 协商成功、写入回显（Write→data 事件）、Resize 生效、Close 幂等不 panic、Open/Close 并发多会话、断线后 Write 返回错误
- **Go（executor）**：`DialClient` 提取后现有测试平移，行为不变
- **前端**：计时 reducer 纯函数单测（事件序列 → 块状态/时长/轮尾统计，含 <1s 阈值与取消路径）；xterm 集成与 tab 交互手动验证
- 沿用总原则：GUI 不写 E2E，产品逻辑在 Go 层覆盖

## 8. 明确不做（本期 YAGNI）

- ProxyJump 跳板链路、主机多上游配置
- extended thinking 推理文本流式展示
- 终端自动重连、会话恢复（重启后终端 tab 不恢复）
- 终端内容/击键审计
- 终端拆独立窗口、分屏
- 对话流 markdown 渲染

---
psy_kind: factual
psy_version: 1
package: internal/term
---

# internal/term

## 概述

`internal/term` 提供内嵌交互终端的 SSH PTY 会话管理：Web 端「>_」按钮对主机开交互式 shell。每会话一条专属 `*ssh.Client`（经 `executor.DialClient` 拨号，不复用 executor.Manager 的命令连接缓存——其探活/驱逐语义会砍掉长挂的交互会话），输出经 `Emitter` 回调外送（webapi 终端端点 termws 桥接为 WS 下行），输入/尺寸/关闭经 Manager 方法上行。包内无 Web 框架依赖，可独立单测。

## 文件

- `term.go` — `Manager`/`Session`：拨号 + RequestPty + Shell、输出泵（base64 + 回放环形缓冲 64KB）、退出看护、写入互斥、按主机拨号串行化、按会话/按主机/全量关闭
- `term_test.go` — 进程内 SSH 假服务器（PTY/shell 放行 + 回显 + window-change 记录）与用例（回显、resize、未知会话、幂等关闭、按主机关闭、FindByHost/Exists、服务器断连、并发压力、同主机顺序复用、同主机并发单拨号、属主隔离）

## API

- `type Emitter func(Event)` — 会话事件回调。被 x/crypto 的 stdout/stderr 拷贝 goroutine 与退出看护 goroutine 并发调用（每会话最多三个并发方），实现必须并发安全且不得无限期阻塞；同一会话 EventData 严格先于 EventExit
- `type EventType int`（`EventData` / `EventExit`）与 `type Event struct{ Type; ID; Data; Err }` — 事件包；Data 为 base64 编码的输出块（终端流含任意二进制转义序列）
- `NewManager(secrets host.SecretStore, emit Emitter) *Manager` — 创建会话管理器
- `(m *Manager) Open(h *host.Host, owner string) (string, error)` — 拨号（15s 全握手超时）+ RequestPty(xterm-256color, 24 行 × 80 列) + Shell，返回**随机 16 字节 hex** 会话 ID（不可枚举）；启动输出泵与退出看护。按「主机+属主」单会话的 GetOrCreate：同主机同属主已有在线会话则幂等返回其 ID，不同属主各开各的（多用户隔离）；per-键互斥锁（锁内重查）串行化并发 Open，消除检查-拨号-注册窗口的双拨号（TOCTOU）
- `(m *Manager) Write(id, data string) error` — 写 stdin（每会话互斥串行化：x/crypto 同流并发写不安全，webapi 的 WS 上行 write/resize 帧各自独立 goroutine）
- `(m *Manager) Resize(id string, cols, rows int) error` — window-change（注意参数序转换为 x/crypto 的 (h, w)）
- `(m *Manager) Close(id string)` — 关闭会话（幂等；stdin/session/client 三层关闭）
- `(m *Manager) CloseHost(hostID string)` — 关闭某主机的全部会话（DeleteHost 时调用）
- `(m *Manager) CloseAll()` — 关闭全部会话（应用退出时调用）
- `(m *Manager) FindByHost(hostID, owner string) (string, bool)` — 返回该「主机+属主」当前打开的会话 ID（单会话约定；webapi 附加语义）
- `(m *Manager) Owner(id string) (string, bool)` — 会话属主反查（webapi WS attach / REST close 的属主校验）
- `(m *Manager) Exists(id string) bool` — 判断会话是否仍在线（webapi 的 WS attach 前校验）
- `(m *Manager) Replay(id string) []byte` — 返回会话回放缓冲快照（最近 64KB，评审 ④-6：webapi WS attach 时补发，open 与 attach 之间的输出不再丢失；会话不存在返回 nil）

## 依赖

**内部包：** `gengar-pro/internal/executor`（DialClient 拨号认证）、`gengar-pro/internal/host`（Host/SecretStore）

**第三方库：** `golang.org/x/crypto/ssh`

**标准库：** `encoding/base64`、`fmt`、`io`、`sync`、`time`

## 设计重点

### 专属连接与生命周期

每会话独占一条连接与一个 PTY，会话即连接——关闭 tab、删除主机、远端退出、应用退出四条路径都汇聚到 `Session.close()`（`sync.Once` 幂等：stdin → session → client 三层关闭）。远端退出由退出看护 goroutine 感知（`sess.Wait()` 返回 → 先 emit `EventExit` 再自回收），因此前端无论哪条路径断开都会收到终态事件。

### 写入互斥

`Session.writeMu` 串行化 stdin 写与 window-change：x/crypto 的 `WriteExtended` 允许并发调用产生交错包（race detector 亦会标记），而 webapi 终端端点（termws）的每个 WS 连接读循环各自是独立 goroutine，多端附加同一 PTY 时击键/粘贴/resize 天然并发。互斥后乱序风险一并消除。

### 按主机拨号串行化

`Manager.hostMu`（惰性建，表本身由 `m.mu` 保护）按主机 ID 互斥整个 Open：锁内先 `FindByHost` 重查，命中即幂等返回既有会话 ID。锁序恒为 hostMu → m.mu。并发「开终端」请求由此收敛为单拨号单会话，调用方（webapi REST open / WS 附加）无需自带检查-再开原子性。

### 回放环形缓冲（评审 ④-6）

输出泵在 emit 前同步 `pushReplay`：每会话保留最近 64KB 输出（超限丢最旧——attach 只需最近上下文，非全量历史）。`Replay(id)` 返回快照，webapi 在 WS attach 后、subscribe 前补发：输出泵在 emit 的 fanout 返回前不会写下一块，补发窗口内不可能有实时帧插队，补发与实时帧顺序即真实输出顺序。此机制修掉两个已知丢失窗口：open 与 attach 之间、attach 前的全部历史。

### 测试策略（无 race detector 环境的替代）

本机 `-race` 因工具链问题不可用（TDM-GCC 与 Go race 运行时不兼容），用两类测试补偿：进程内假服务器真握手（PTY 协商/回显/window-change 尺寸断言锁死 x/crypto 的 (h, w) 参数序陷阱）+ 并发压力用例（6 goroutine × 20 轮 open/write/resize/close，验证无死锁无 panic）；已知_hosts 重定向到带 pid 的临时路径隔离 TOFU。

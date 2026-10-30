---
psy_kind: factual
psy_version: 1
package: internal/executor
---

# internal/executor

## 概述

`internal/executor` 统一 gengar-pro 的命令执行接缝：`Executor` 接口抽象"在某一台目标机上跑命令/写文件"，`Local` 绑定本机、`SSH` 绑定一条远端连接；`Manager` 按主机缓存连接并探活复用。插件层只依赖本接口，不感知目标在本地还是远程。

## 文件

- `executor.go` — `Executor` 接口与 `Local` 实现（bash -c / argv exec / os.WriteFile），含兜底超时逻辑
- `ssh.go` — `DialClient` 拨号（密码/密钥认证、全握手 deadline；executor 与 term 共用的唯一拨号入口）、`DialHost` 与 `SSH` 执行器（session 执行 + SFTP 写文件 + POSIX 单引号 argv 引用）
- `knownhosts.go` — TOFU 主机密钥策略（首连记录指纹、已记录则严格校验）、`ErrHostKeyMismatch` 哨兵、`SetKnownHostsPath` 重定向
- `manager.go` — `Manager`：按 host.ID 缓存 SSH 连接，探活看门狗 + 死连接重拨
- `local_test.go` / `ssh_test.go` / `manager_test.go` — 单元测试
- `testserver_test.go` — 进程内 SSH 假服务器（密码认证 + exec 回显 + SFTP 子系统），SSH 单测无需 Docker

## API

- `type Executor interface` — 一次目标绑定的执行器：`RunShell(ctx, cmd)`（目标机 shell 语义）、`RunArgv(ctx, name, args...)`（argv 语义，参数不经 shell 重新解释）、`WriteFile(ctx, path, content)`（写文件 0644，父目录由调用方保证）
- `NewLocal(timeout time.Duration) *Local` — 创建本机执行器；timeout 在调用方 ctx 无 deadline 时兜底，零值不设兜底
- `type Local struct` — 本机实现（RunShell 依赖 bash 在 PATH 中；Windows 上 .bat/.cmd 经 cmd.exe 解析为标准库行为）
- `DialClient(h *host.Host, secrets host.SecretStore, timeout time.Duration) (*ssh.Client, error)` — 按 AuthConfig 建立一条 SSH 连接。拨号认证的唯一入口：executor（RunShell/SFTP）与 term（交互 PTY 会话）共用，认证/TOFU 行为一处维护
- `DialHost(h *host.Host, secrets host.SecretStore, timeout time.Duration) (Executor, error)` — 按 AuthConfig 建立 SSH 连接并返回绑定该连接的执行器；密钥路径支持 `~` 前缀
- `DialHostOnce(h *host.Host, secrets host.SecretStore, timeout time.Duration) (Executor, func(), error)` — 一次性连接：同 DialHost 但不进 Manager 缓存，返回执行器与关闭函数（webapi 表单内测试用未保存参数拨号，用后即弃）
- `type SSH struct` — 绑定一条 `*ssh.Client` 的执行器；RunArgv 以 POSIX 单引号引用后交远端 shell
- `ErrHostKeyMismatch` — 主机密钥与 known_hosts 记录不符（疑似中间人）的哨兵错误，可用 `errors.Is` 判别
- `SetKnownHostsPath(path string)` — 重定向 known_hosts 位置，须在任何拨号前（启动期单线程）调用；空串恢复默认解析。产品入口（gengar-server / gengar-headless）在启动期设为 `<data>/known_hosts`（TOFU 记录随数据目录隔离）；测试/集成可重定向到临时目录。路径解析优先级：本函数显式重定向（两入口恒走此档）> `GENGAR_KNOWN_HOSTS` 环境变量（兜底）> 默认 `~/.gengar-pro/known_hosts`
- `NewManager(secrets host.SecretStore, timeout time.Duration) *Manager` — 创建连接管理器
- `(m *Manager) ExecutorFor(h *host.Host) (Executor, error)` — 返回主机执行器：缓存命中且探活通过则复用，否则关闭旧连接重拨（互斥保证同主机不并发重拨）
- `(m *Manager) Close(hostID string)` — 关闭指定主机的缓存连接
- `(m *Manager) CloseAll()` — 关闭全部缓存连接
- `(m *Manager) ActiveConns() int` — 当前缓存连接数

## 依赖

**内部包：** `gengar-pro/internal/host`（Host/AuthConfig/SecretStore）

**第三方库：** `golang.org/x/crypto/ssh`、`golang.org/x/crypto/ssh/knownhosts`、`github.com/pkg/sftp`

**标准库：** `bytes`、`context`、`errors`、`fmt`、`net`、`os`、`os/exec`、`path/filepath`、`strconv`、`strings`、`sync`、`time`

## 设计重点

### argv 语义的跨传输等价

`RunArgv` 在 Local 下是真正的 argv exec；SSH 下把参数以 POSIX 单引号安全引用（`'` → `'\''`）拼接为远端命令串，假设远端为 POSIX 兼容 shell，语义等价——注入安全，但参数中的 NUL 字节会被远端 shell 截断，不可携带。

### 全握手 deadline

不用 `ssh.Dial`（它只给 TCP 段设超时，kex/认证阶段无期限，对接上挂起的服务器会永久阻塞）：自行接管连接生命周期，TCP 拨号后给整个握手设 `SetDeadline`，成功后清除，业务期由各命令的 ctx 控制。ctx 取消时关闭 session / SFTP client 中断远端命令（SFTP 协议本身不看 ctx）。

### TOFU 首信策略

新指纹写入 `~/.gengar-pro/known_hosts` 并放行；已有记录但不匹配则拒绝连接（`ErrHostKeyMismatch` 双 `%w` 包装，哨兵供 UX 判别、`knownhosts.KeyError` 保留在链上）。并发首连同一新主机的竞态只产生重复行，无害。remote 参数必须透传（x/crypto 内部无条件调 `remote.String()`，nil 会 panic）。

### 探活看门狗防黑洞

探活（openssh keepalive 往返）全程持有 Manager 互斥锁，连接黑洞化（TCP 未断但对端永不回复）时 `SendRequest` 会阻塞到 OS 层 keepalive 超时，不设期限一台主机就冻结所有主机的 ExecutorFor。看门狗期限取 `min(timeout, 5s)`：健康网络一次往返毫秒级，又远小于 TCP 栈默认黑洞检测期。被判死的连接由随后的 close 解除被放弃 goroutine 的阻塞。

### 输出语义兼容

Local 的 `run` 合并 stdout+stderr（stdout 在前），与旧 plugin helpers 输出逐字节兼容，保证插件返回内容与历史行为一致。兜底超时故意不提前 cancel（timer 存活至 deadline，泄漏窗口 = timeout，可接受），避免 go vet lostcancel 之外再引入提前取消竞态。

# 终端 ZMODEM 文件传输设计

日期：2026-11-25 · 状态：已批准（方案 A）

## 背景与目标

gengar-pro 的内嵌 SSH 终端（xterm.js + WS 中继 + `internal/term` PTY 会话）目前不支持文件传输：不能拖拽上传，`rz`/`sz`（ZMODEM，lrzsz 实现）命令收到的协议帧被当普通终端输出渲染成乱码。

目标：终端支持 `rz`/`sz` 原生工作 + 拖拽文件上传（自动落到 shell 当前目录），体验对齐 XShell。

### 已确认约束

- 远端主机**全部装有 lrzsz**（用户确认）——ZMODEM 端到端方案成立，不做 SFTP 后备。
- 文件规模以配置/日志为主，**≤100MB**——浏览器内存 Blob 攒文件可接受，不做流式落盘。

### 现状事实（选型依据）

| 通路 | 现状 | 二进制安全性 |
|---|---|---|
| 下行 PTY→浏览器 | `termFrame{t:"data", data:<base64>}`，输出泵 base64 编码 | **安全**，ZMODEM 帧原样到达 |
| 上行 浏览器→PTY | `JSON.stringify` 字符串 → WS UTF-8 → Go `[]byte(data)` | **不安全**：0x80–0xFF 字节被 UTF-8 改写为多字节序列，破坏 ZMODEM 帧 |

ZMODEM 协议本就为「终端字节流」设计（与 XShell/SecureCRT 的 rz/sz 同构），服务端中继对其完全透明。方案对比后选定 **A：ZMODEM 端到端**（B：SFTP 通道——不满足 rz/sz 原始诉求且后端新面大；C：混合——YAGNI）。

依赖：`zmodem.js` 0.1.10（npm，Apache-2.0，ttyd 同款生产验证；纯浏览器端状态机，服务端零新依赖）。

## 架构与数据流

```
上传：拖拽/文件选择 ──→ TerminalPane ──→ rz\r 注入 + zmodem.js 打包
      → TermConn.sendBytes (分块≤32KB, b64) → WS write 帧 → webapi 解码 → PTY stdin
      → 远端 lrzsz rz 落盘（shell 当前目录）

下载：远端 sz → PTY 输出(ZMODEM 帧) → 输出泵(已有) → WS data 帧(已有 b64)
      → TerminalPane 解码 → Sentry.consume ──非 ZMODEM──→ xterm 渲染
                                        └─ZMODEM─→ 接收会话 → Blob → a[download] 保存
```

`Sentry`（zmodem.js 内建）是唯一新增流处理节点：下行字节先喂 `consume()`，非协议字节透传 `to_terminal` 回调进 xterm；检测到协议即经 `on_detect` 回调，确认后接管双向流。

## 服务端改动（仅上行帧编码一处）

- 上行 `write` 帧 `data` 语义改为 **base64**；webapi write 分支 `base64.StdEncoding.DecodeString` 后传 `termMgr.Write(id, []byte)`。
- `term.Manager.Write` 签名 `string → []byte`；`termws.go` 的 `termConn` 接口与单测假实现同步。
- 解码失败 → 丢弃该帧（客户端 bug，终端流无错误回传通道）。
- 其余零改动：路由/属主校验/reaper/审计不涉及。前后端同进程部署（StaticHandler 同端口），无版本偏斜。
- **明确不改**：termHub 订阅通道缓冲（64 帧 ≈2MB）与慢消费者丢帧语义——ZMODEM 有 ZRPOS 重传自愈；若大文件下载验收发现频繁重传，调大该缓冲是预留的第一杠杆（不预做）。

## 前端改动

模块边界（各自独立可测）：

| 单元 | 职责 |
|---|---|
| `src/api/events.ts` 扩展 | `send`（文本）统一改走 base64（TextEncoder → btoa）；新增 `sendBytes(Uint8Array)`——协议字节上行，内部按 32KB 分块（b64 后 + JSON 开销 < 服务端 64KB 单帧读上限） |
| `src/zmodem.ts`（新） | zmodem.js 的唯一 import 面（门面）：`create({ writeTerm, sendPty })` → `{ consume, onDetect, abort }`；传输状态纯函数归约（idle/sending/receiving/done/error + 文件名/进度/速度）——vitest 直测，组件不直接依赖 zmodem.js 内部 |
| `src/components/TerminalPane.tsx` | 下行改经 `consume`；传输中丢弃 `onData`（Ctrl+C 映射为取消）；拖拽 → `conn.send("rz\r")` → 检测就绪 → `Browser.send_files(files)`；下载 offer 自动接收（XShell 行为）→ Blob → `a[download]` 保存 |
| `src/styles.css` | 传输状态条（文件名/进度/速度/取消按钮；耿鬼紫主题；底部浮层不遮终端历史） |

- 拖拽**不弹确认**直接执行（electerm/SecureCRT 惯例）；状态条常驻提示进行中。
- 多文件 offer（`sz a b c`）全部自动接收，逐个触发浏览器下载。
- zmodem.js 经 Vite 依赖预构建引入（CJS interop；src 入口或 dist bundle 以实际可用为准）。
- `zmodem.ts` 由 TerminalPane 静态 import——TerminalPane 本身已 React.lazy 代码分割，zmodem.js 随终端 chunk 加载，不进首包。

## 错误处理与边界

| 场景 | 行为 |
|---|---|
| 传输中 WS 断开/exit | `onExit` 调 `abort()`（发 ZABORT 令远端 lrzsz 尽量干净退出）；重连后回放缓冲（64KB）里的二进制残渣由 Sentry 消化/拒绝，最坏表现为一次失败提示——接受 |
| 传输中取消 | 状态条按钮 / Ctrl+C → `abort()` |
| 用户在 vim/less 中拖拽 | `rz` 字符敲进编辑器（与 XShell 同病）；接受，不做提示符检测 |
| 传输中另一端 attach 同 PTY | 字节流共享冲突——已知限制，不处理（场景边缘） |
| lrzsz 未装的主机 | `rz` 报 command not found，Sentry 无检测即无 UI——天然退化 |
| 文件名含 ZMODEM 头字符 | 协议层「投毒」极端场景，zmodem.js README 明示不处理 |

## 审计

ZMODEM 对服务端是纯 PTY 字节流，**无新审计事件**——与终端击键/输出同级不可见，与现有审计姿态一致（term_open/term_close 既有事件不变）。

## 测试策略

- **Go**：`termws_test.go`——write 帧 base64 解码后字节原样到达 `Write([]byte)`（假实现记录断言）；非法 base64 丢弃。`term` 包——`Write([]byte)` 签名适配 + 二进制帧回显用例（进程内假 SSH 服务器）。
- **vitest**：`send` 的 UTF-8→base64 正确性；`sendBytes` 32KB 分块边界；传输状态归约纯函数；（若 zmodem.js 可在 node 加载）手造 ZRQINIT 序列驱动门面的检测/透传测试。
- **人工验收清单**：rz 上传、sz 多文件下载、拖拽、取消、断线恢复、100MB 大文件 LAN 压测。
- 三门禁：`npm test` + `go test ./...` + `npm run build`（tsc 类型检查）。

## 已知限制汇总

1. 下载在浏览器内存攒 Blob（≤100MB 约束内可接受）。
2. 传输中断开 tab/WS 即中断传输（ZMODEM 点对点流协议，无断点续传 UI）。
3. 传输期间多端附加同一 PTY 会互相干扰。
4. 服务端看不到传输内容，无审计。

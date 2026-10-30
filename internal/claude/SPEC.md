---
psy_kind: factual
psy_version: 1
package: internal/claude
---

# internal/claude

## 概述

`internal/claude` 包是项目中与 Anthropic Claude API 交互的 HTTP 客户端封装层。它支持两种调用方式：非流式的 `SendMessage` 和**流式的 `MessagesStream`**（按 SSE 协议逐事件产出 `StreamEvent`，供 agent 主循环边收边渲染）。两路建流均带可重试错误的指数退避重试（429/5xx/网络类；4xx 不重试）；流式路径用独立无超时 client + 读空闲看门狗（90s 无字节判死），长输出不被整请求超时误杀。该包负责构建请求（系统提示、多轮消息、工具调用）、解析 JSON 响应或 SSE 事件流，并提供长文本截断工具函数。仅使用 Go 标准库，无第三方依赖。

## 文件

- `client.go` — 核心客户端实现：`Client` 结构体、构造函数（非流式 120s 整请求超时）、`streamHTTP()`（流式专用 client：仅约束拨号 30s/TLS 15s/首字节 60s，无整体 Timeout）、`retryRetryable`（3 次指数退避 + ±30% 抖动，`isRetryable` 判定 429/5xx/网络类）、`SendMessage`、`DefaultMaxTokens` 常量（16384——4096 会掐断巡检报告类长输出），以及 `TruncateOutput` 截断工具函数
- `stream.go` — 流式客户端：`StreamEvent`/`EventType`/`BlockKind` 类型定义、`MessagesStream`（建流可重试；打开后不重试——半途内容已消费）、`parseSSE` SSE 帧解析器（idleReader 读空闲看门狗包装：90s 无字节返回 `idleTimeoutError` 判链路死亡；数据持续到达的慢流不误杀）、tool_use 的 `input_json_delta` 碎片累积
- `types.go` — 所有数据类型定义：`ContentBlock`、`Message`、`Tool`、`Usage`、`APIResponse`（导出）和 `apiRequest`（内部未导出，`Stream bool` 字段控制是否走 SSE）
- `client_test.go` — 客户端集成测试：覆盖纯文本响应、工具调用响应、5xx 重试、4xx 不重试、429 重试恢复、Temperature 字段两种场景
- `stream_test.go` — 流式客户端与 SSE 解析的单元测试（含看门狗超时/慢流不杀回归）
- `truncate_test.go` — 截断函数测试：验证短文本不截断、长文本正确截断并保留首尾
- `types_test.go` — 类型序列化/反序列化测试：覆盖 Message、ContentBlock、APIResponse

## API

### 结构体

- **`ClientConfig`** — 客户端配置（APIKey、BaseURL、Model、Temperature 可选指针）
- **`Client`** — Claude API HTTP 客户端
- **`ContentBlock`** — 内容块，支持 text、tool_use、tool_result 三种类型
- **`Message`** — 聊天消息，Role 为 "user"/"assistant"，Content 为 `interface{}`
- **`Tool`** — 工具定义（Name、Description、InputSchema）
- **`Usage`** — Token 用量统计（InputTokens、OutputTokens）
- **`APIResponse`** — 非流式 API 响应结构（ID、Type、Role、Content、StopReason、Usage）
- **`StreamEvent`** — 流式事件载体；字段语义由 `Type` 决定：`Block`+`Index` 标识内容块，`Text` 载 text delta，`ToolID`/`ToolName`/`ToolInput` 在 `Block=ToolUse` 时填充（start 填 ID+Name、stop 填完整 `ToolInput`），`StopReason` 来自 `message_delta`，`Error` 来自 `EventError`

### 流式类型（stream.go）

- **`EventType`** (int) — 事件族枚举：`EventMessageStart`、`EventContentBlockStart`、`EventContentBlockDelta`、`EventContentBlockStop`、`EventMessageDelta`、`EventMessageStop`、`EventError`
- **`BlockKind`** (int) — 内容块类型枚举：`BlockUnknown`、`BlockText`、`BlockToolUse`
- **`ErrStreamInterrupted`** (var error) — 表示流在 `message_stop` 之前结束；调用方应据此把已累积的部分文本标记为"[stream interrupted]"

### 函数

- `NewClient(cfg ClientConfig) *Client` — 创建客户端实例（非流式 120s 整请求超时）
- `(c *Client) SendMessage(ctx, system, messages, tools) (*APIResponse, error)` — 非流式发送消息到 Claude Messages API；可重试错误（429/5xx/网络）重试 3 次
- `(c *Client) MessagesStream(ctx, system, messages, tools) (<-chan StreamEvent, func(), error)` — 流式发送：建流失败可重试（同上语义）；打开后走 streamHTTP（无整体 Timeout）+ 读空闲看门狗，返回事件 channel 与 cancel 函数；goroutine 在解析完成或出错后关闭 channel；HTTP 4xx 立即返回错误不进 channel
- `TruncateOutput(output, maxLen, headLen) string` — 对超出 maxLen 的输出进行首尾截断

### 常量

- `DefaultMaxTokens = 16384` — 单次响应输出上限（两路共用）
- `streamReadIdleTimeout = 90s` — 流式读空闲看门狗阈值
- `retryAttempts = 3`、`retryBaseDelay = 100ms` — 可重试错误的退避参数（实际等待 = base<<attempt + ±30% 抖动）

### 错误类型

- `idleTimeoutError` — 流读空闲超时（区别于普通网络错误，调用方可归因「链路死亡」）

## 依赖

**标准库：** `bufio`、`bytes`、`context`、`encoding/json`、`errors`、`fmt`、`io`、`net/http`、`strings`、`time`

**第三方库：** 无。零外部依赖。

**内部包：** 无。

## 设计重点

### SSE 流式协议解析

`parseSSE` 是 stream.go 的核心。它逐行扫描响应体，按 SSE 帧规范累积 `data:` 行（其他字段 event:/id:/retry: 全部忽略，以 JSON 载荷里的 `type` 字段为唯一派发键），空行触发 `flush` 把累积的 JSON 解析成对应 `StreamEvent` 投递到 channel。

关键细节：

- **tool_use 输入碎片累积**。`content_block_delta` 里的 `input_json_delta` 不直接对外发事件——它在内部按 index 累积到一个 `toolBuf.input` 字符串里，直到对应的 `content_block_stop` 才把组装好的完整 JSON 作为 `StreamEvent.ToolInput` 一次性发出。这样 agent 主循环无需处理 JSON 拼接。
- **BlockKind 跨事件追踪**。`content_block_delta` 的 JSON 载荷本身不带 block 类型（只有 `delta.type`），解析器维护一个 `blockKinds map[int]BlockKind`，在 start 时记下、在 delta/stop 时查回，从而 `StreamEvent.Block` 字段在所有 content_block_* 事件里都有意义。
- **ctx 取消优先**。每个 `send` 都用 `select` 同时监听 channel 与 `ctx.Done()`，cancel 后解析立即中止并返回 `ctx.Err()`；HTTP body 在 goroutine 的 `defer` 里关闭。
- **EOF 不是错误**。`scanner.Scan` 自然结束（EOF）时 `parseSSE` 返回 nil，由调用方通过观察 channel 上是否收到 `EventMessageStop` 来判断是干净结束还是中断（因此有 `ErrStreamInterrupted` 这个哨兵供需要区分的场景使用）。

### 指数退避重试（建流两路共用）

`retryRetryable` 抽出为公共函数：429/5xx/网络类错误重试 3 次（base 100ms 指数位移 + ±30% 抖动防多实例同步重试风暴）；400/401 等客户端错误重试无意义立即返回。`SendMessage` 全请求可重试；`MessagesStream` 只重试建流阶段——流打开后的中断不重试（半途内容已消费，重放需上层重建整个 turn）。

### 流式超时模型（评审 ②-2）

流式路径不复用 120s 整请求超时的 client——那会把 >2 分钟的合法长报告连着成功一半的流一起杀掉。改用：独立 client 仅约束拨号/TLS/首字节；读侧 `idleReader` 看门狗（goroutine + timer，90s 无任何字节即 `idleTimeoutError`）。「整请求超时」看总量，「读空闲」看间隔——数据持续到达的慢流不误杀，真死链 90s 内判死。

### Temperature 指针语义

`ClientConfig.Temperature` 使用 `*float64`，`nil` 时通过 `omitempty` 从请求 JSON 中省略，显式设 0 时发送 `0.0`，精确匹配 API 语义。

### Stream 字段开关

`apiRequest.Stream bool` 加 `omitempty`：`SendMessage` 留空（请求 JSON 不带 `stream` 字段，走非流式），`MessagesStream` 置 true（请求 JSON 带 `"stream":true`，服务端返回 SSE）。同一个 `apiRequest` 结构体服务两种模式。

### Content 字段多态

`Message.Content` 为 `interface{}`，允许简单字符串或 `[]ContentBlock`，适配 Claude API 的灵活消息格式。

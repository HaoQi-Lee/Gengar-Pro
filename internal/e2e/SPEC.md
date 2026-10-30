---
psy_kind: factual
psy_version: 1
package: internal/e2e
---

# internal/e2e

## 概述

`internal/e2e` 是 gengar-pro 多主机桌面架构的端到端套件：本地 SSE 桩扮演 Anthropic Messages API（按脚本回放工具调用序列），Docker sshd 容器扮演真实目标机，`cmd/gengar-headless` 子进程扮演整装产品入口；断言数据源 = headless 的 stdout（plain sink 全量输出）+ 数据目录里的 `audit.jsonl`（工具名 / Hosts / IsError）。全部源文件挂 `//go:build e2e` 标签，不进生产构建；旧单机 CLI 套件（本地沙箱 + observer 事件流 + HTML 报告链 + `cmd/e2e-report`）已整体删除。

## 文件

- `stubllm.go` — Anthropic Messages SSE 桩（httptest）：脚本第 N 回合对应第 N 个 `/v1/messages` 请求，工具结果回传自然推进脚本；帧格式逐字节对照 `internal/claude/stream_test.go` 的 `sampleSSE`（有序 struct 序列化保证 JSON 键序一致）；工具输入经 `input_json_delta` 按符文边界分片；脚本耗尽发协议 `error` 事件
- `stubllm_test.go` — 桩单测（无需 Docker）：真实 `internal/claude` 流式客户端打桩全链路（事件序列/工具输入重组/工具结果回传）、原始帧文本逐串对照、405/404 拒绝、脚本耗尽
- `sshd.go` — Docker sshd 容器夹具：与 `internal/integration` 共用 `sshd.Dockerfile`（内置 root:gengar-it-pass），`-P` 随机端口，`t.Cleanup` 清容器；`startHosts(t, n)` 起 n 台。Docker CLI 缺失或守护进程未起 → `t.Skip`
- `harness.go` — 用例脚手架：`host.OpenStore` 播种主机清单（密码走 `--secrets` 引用，不碰钥匙串）、`go build` 缓存编译 headless、子进程 env 注入（`ANTHROPIC_BASE_URL`/`ANTHROPIC_API_KEY`/`GENGAR_COMMAND_TIMEOUT`/`GENGAR_KNOWN_HOSTS`）、`audit.jsonl` 读取
- `cases.go` — YAML 用例（schema v2）加载校验 + 八种断言的纯函数求值
- `cases_test.go` — 用例加载与断言求值的表驱动单测（无需 Docker）
- `e2e_test.go` — 入口：遍历 `testcases/*.yaml`，llm=stub 起桩注入 / llm=real 无 `ANTHROPIC_API_KEY` 则 Skip / Docker 不可用统一 Skip
- `testcases/` — YAML 用例：E01（单机 system_info 最小闭环）、E02（双机 hosts:["all"] fan-out，audit 单条 Hosts=2）、E03（高危命令人工确认拒绝）、E04（标签定向 Hosts 集合精确相等）、E05（记忆跨会话 runs:2）、E06（stop_reason=max_tokens 截断——部分文本保留 + 告警不静默，回归 f865015）、E07（工具调用省略 hosts——默认目标兜底分发，回归目标范围语义链）、R01（真机冒烟，需 API key）

## API

### 桩（StubLLM）

- `Turn{ToolCalls, FinalText, Text, StopReason}` — 脚本回合：ToolCalls 非空发 tool_use 块（stop_reason=tool_use，Text 作可选前导文本块），否则发文本块（stop_reason=end_turn）；StopReason 非空时覆盖默认停机原因，复刻异常收束形态（max_tokens 截断等）
- `ToolCall{ID, Name, Input}` — Input 为 nil 发空对象
- `startStubLLM(t, script)` — 起桩 + `t.Cleanup` 关闭
- `(s) URL() / RequestCount() / Request(i) / APIKey(i)` — 注入地址与请求录制（断言工具结果回传、鉴权头）

### 夹具与脚手架

- `startSSHd(t) int` / `startHosts(t, n) []HostSpec` — sshd 容器（返回映射端口 / n 台规格）
- `SeedHost{Name, Addr, Port, User, Tags}` / `seedHosts(t, dataDir, hosts)` — 播种主机清单到 `dataDir/gengar.db`（与 headless 的 `appcore.OpenAt` 同库）
- `runHeadless(t, dataDir, baseURL, apiKey, inputs, timeout, autoYes) string` — 启动 headless 子进程跑完一轮输入（恒传 `--pin-all`——全部用例都在「默认目标 = 清单全集」下跑，E07 即测此语义），返回合并 stdout+stderr
- `readAudit(t, dataDir) []audit.Entry` — 解析 `dataDir/audit.jsonl`（缺失 = 空）
- `buildHeadless(t)` / `repoRoot(t)` — 编译缓存与仓库根定位

### 用例模型与断言（八种）

- `e2eCase` — `name/description/llm(stub|real)/hosts/tags/auto_yes/script/inputs/runs/assertions`；`scriptTurn` 含可选 `stop_reason`
- `loadCase(path)` — 加载 + 校验（llm 合法性、stub 必有 script、hosts≥1、min_count 归一）
- `scriptToTurns(script)` — YAML 脚本转桩 Turn（id 缺省补齐、stop_reason 透传）
- `evalAssertion(a, stdout, entries, dataDir)` — 求值：`tool_called{name,min_count}`、`tool_not_called{name}`、`response_contains{matches}`（stdout 正则）、`response_not_contains{matches}`、`audit_hosts{tool,count}`（存在一条 audit Hosts 长度=count）、`audit_host_names{tool,names}`（Hosts 集合精确相等，序无关）、`audit_rejected{tool}`（approved=false）、`memory_contains{host,matches}`（数据目录记忆文件正则）

## 依赖

**内部包：** `gengar-pro/internal/audit`（Entry）、`gengar-pro/internal/claude`（真实流式客户端打桩）、`gengar-pro/internal/host`（OpenStore 播种）

**第三方库：** `gopkg.in/yaml.v3`

**外部系统：** Docker（CLI + 守护进程；不可用 → Skip）、Go 工具链（harness 现场编译 headless）、`cmd/gengar-headless`（被测入口）、`internal/integration/sshd.Dockerfile`（镜像源）

## 设计重点

### 桩协议：真实 wire 格式

桩只认请求序号不做对话理解，但帧按上游真实 SSE 形态发射（event:/data: 双行、JSON 键序与 `sampleSSE` 逐字节一致、input_json_delta 分片重组）——`internal/claude` 的 parseSSE 重组路径在 E2E 下被真实走到，而非只测 Happy path 单帧。`stubllm_test.go` 用真实客户端打桩，是「桩 ≈ API」的最强验证。非流式请求打到桩会收到 SSE 体（JSON 反序列化失败），本身就是测试信号。

### 断言 = stdout + audit 双源，纯函数求值

stdout 覆盖面向用户的最终呈现（plain sink 文本），audit.jsonl 覆盖工具调用事实（名称/目标主机数/错误标记）；断言是 `(assertionSpec, string, []audit.Entry) → error` 纯函数，表驱动单测不依赖 Docker / 桩 / headless。远端终态断言（docker exec 查文件等）为 YAGNI 边界，需要时再加。

### 三层 Skip 语义

llm=real 且无 `ANTHROPIC_API_KEY` → Skip；Docker CLI 缺失 → Skip；守护进程未起（`docker info` 失败）→ Skip。桩/断言单测在无 Docker 环境照常运行——本机常态即「单测绿 + 用例 Skip」。

### 进程隔离

每个用例独立 `t.TempDir()` 数据目录（gengar.db / audit.jsonl / known_hosts 全部就地隔离）；子进程 env 剔除继承的 `ANTHROPIC_*` 后显式注入，开发机真实 key / 网关地址不泄漏进用例；密码经 `--secrets ref=pass` 内联注入，不碰系统钥匙串；TOFU 库经 `GENGAR_KNOWN_HOSTS` 隔离进数据目录（SSH 拨号在子进程，测试进程的重定向够不着，必须走 env）。

### 桩为主 + 真机冒烟

确定性靠桩（脚本回放，CI 友好），真实模型兼容靠 llm=real 冒烟用例（env 双门：API key + Docker）。

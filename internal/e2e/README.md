# internal/e2e — gengar-pro 端到端套件

桩 LLM（本地 SSE 桩按脚本回放工具调用）+ Docker sshd 容器（真实 SSH、真实执行）+ `cmd/gengar-headless` 子进程（整装产品入口），断言三源：headless 的 stdout（plain sink 全量文本）、数据目录里的 `audit.jsonl`（工具名 / Hosts / Approved / IsError）、跨进程文件（`memory/<host>.json`）。全部源文件挂 `//go:build e2e`，不进生产构建。

## 前置

- **Docker Desktop 运行中**（CLI 在且守护进程可达）。不可用 → 全部用例统一 `t.Skip`（与 `internal/integration` 同一约定），桩与断言单测照常运行——无 Docker 的开发机就是这一档。
- `llm: real` 的用例（R01）另需 `ANTHROPIC_API_KEY`；未设 → 该用例 Skip。桩用例不消耗任何 API 配额。
- Go 工具链：harness 现场编译 `cmd/gengar-headless`（进程内只编一次）。

## 运行（三档）

```bash
# 1) 无 Docker 可跑的部分：桩与断言单测（加载校验 / 断言求值 / SSE 帧形态）
go test -tags e2e ./internal/e2e/ -run 'TestLoadCase|TestEval|TestScriptToTurns|TestStubLLM' -v

# 2) 全量：桩用例（需 Docker）+ R01 真机（需 Docker + ANTHROPIC_API_KEY）
go test -tags e2e ./internal/e2e/ -v

# 3) 单用例：-run 匹配子测试名（= YAML 文件名的前缀）
go test -tags e2e ./internal/e2e/ -run 'TestE2ECases/E04' -v
```

## 桩机制

`stubllm.go` 用 `httptest` 扮演 Anthropic Messages API：每个用例给一份脚本（YAML `script`），脚本第 N 回合对应第 N 个 `/v1/messages` 请求——回合要么发 `tool_use` 块（Agent 循环真实执行工具、把结果回传），要么发终文本（end_turn 收束）。终回合可加 `stop_reason:` 覆盖默认停机原因，复刻异常收束形态（E06 用 `max_tokens` 复刻「报告被上限掐断」）。帧格式逐字节对照 `internal/claude/stream_test.go` 的 `sampleSSE`（事件序、JSON 键序、`input_json_delta` 分片全一致），所以 `internal/claude` 的 SSE 解析路径在 E2E 下被真实走到。收益：**确定性**（同一脚本永远产生同一对话形状，失败可复现）与**零 API 成本**（CI 友好）；真实模型兼容由 R01 冒烟兜底。多轮用例（`runs:`）每轮独立起桩，脚本从头回放。

## 用例清单

| 用例 | 场景 | 核心断言 |
|---|---|---|
| E01-basic-os-info | 单机系统信息最小闭环 | `tool_called system_info` + 回复关键词 |
| E02-fanout | 双机 fan-out（`hosts:["all"]`） | `audit_hosts count=2`（单条记录两台） |
| E03-safety-blacklist | 高危命令 `rm -rf /` 被确认门控拦截（`auto_yes:false`） | `audit_rejected`（approved=false）+ stdout 无工具执行行 |
| E04-tag-targeting | 标签定向（`hosts:["web"]` 恰为两台 web 机） | `audit_host_names`（Hosts 集合精确相等，序无关） |
| E05-memory-cross-session | 记忆跨会话（`runs:2` 两进程共用 DataDir） | `memory_contains`（直查 `memory/e2e-host-1.json` 落盘锚点） |
| E06-max-tokens-truncation | 终回合 `stop_reason: max_tokens`（报告被上限掐断，回归 f865015） | `response_contains` 部分文本保留 + `被截断` 告警不静默 |
| E07-default-target-omit | 工具调用**省略 hosts**——默认目标兜底（harness 恒传 `--pin-all`，回归目标范围语义链） | `audit_hosts count=2`（省略 = 全部默认目标） |
| R01-real-smoke | 真机冒烟（真实 API，需 key） | `tool_called system_info` + OS/内存关键词（宽匹配） |

E03 说明：当前安全模型是两级（Safe/Risky），高危黑名单只给确认弹窗加注记、不做硬拒绝；`--yes` 下高危命令会被自动批准执行。因此 E03 关掉 `--yes` 走「人工确认拒绝」路径，断言落在 audit 的 `approved=false` 上；若未来恢复硬 Forbidden（sink 打印「禁止」），应把断言改回 `response_contains 禁止`。

## 断言类型（八种）

`tool_called{tool,min_count}`、`tool_not_called{tool}`、`response_contains{matches}`、`response_not_contains{matches}`（stdout 正则）、`audit_hosts{tool,count}`（存在一条 Hosts 长度=count）、`audit_host_names{tool,names}`（存在一条 Hosts 集合==names，序无关）、`audit_rejected{tool}`（存在一条 approved=false）、`memory_contains{host,matches}`（`dataDir/memory/<host>.json` 内容正则命中）。断言是纯函数，表驱动单测见 `cases_test.go`；坏正则在 `loadCase` 加载期即失败。

## 用例清单（2026-11 扩充后）

E01-E07 不变（见上表）。新增：

| 用例 | 场景 | 关键断言 |
|---|---|---|
| E08-redis-tools | redis 三工具链（容器内预装 redis + 6 键 dump） | `audit_field {tool: redis_rdb, output_re: 总键数 6}` |
| E09-multi-turn-chain | 多轮工具链（runs:2，write→read 跨进程记忆链） | 顺序正则 + 缩进 JSON 形态锚点（防桩假绿） |
| E10-error-path | hosts 引用不存在主机 | resolver 文案 + `audit_field is_error=true` |

断言类型增至**九种**：新增 `audit_field{tool,user,session_re,output_re,input_re,is_error,count}`（字段级匹配；user/is_error/count 指针三态，count 缺省 >=1、显式 0=不得存在）。

## webapi 档（internal/e2e/webapi/）

驱动真实 `gengar-server` 子进程 + HTTP/WS 真客户端的 Web 层 E2E（W01 登录/限速、W02 主机 CRUD、W03 chat 确认流、W04 断线自动取消+重连续传、W05 多用户隔离；冒烟 TestServerSmoke 不需要 Docker）：

```bash
go test -tags e2e ./internal/e2e/webapi/ -v            # 全量
go test -tags e2e ./internal/e2e/webapi/ -run W03 -v   # 单用例
```

详见 `internal/e2e/webapi/SPEC.md`。

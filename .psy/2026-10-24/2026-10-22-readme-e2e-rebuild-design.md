# gengar-pro 设计文档：README 全面更新 + E2E 套件重建

- 日期：2026-10-22
- 状态：已评审通过（E2E 策略=桩 LLM 为主 + 真机冒烟，经用户确认）
- 参考：桌面端设计 §7 测试策略（`.psy/2026-10-21/2026-10-20-gengar-pro-desktop-design.md`）

## 1. 背景

两件事：README 仍以退役的单机 CLI 为主体（构建/使用/快捷键/批量模式全过时，顶部公告承诺的「随新入口重写」到期）；`internal/e2e` 是单机 CLI 时代的死套件（依赖已不存在的 `cmd/gengar` 二进制与本地执行沙箱），需要按多主机桌面架构重建。

## 2. E2E 重建设计

### 2.1 决策

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| V1 | LLM 驱动 | **桩 LLM 为主 + 真机冒烟**：本地 SSE 桩服务器（httptest）扮演 Claude API，按脚本回放工具调用序列；另留 1-2 个真 API 冒烟用例（`ANTHROPIC_API_KEY` 存在才跑） | 全真 API（慢/贵/不确定，CI 不友好）；纯桩（验证不到真实模型兼容） |
| V2 | 目标主机 | **Docker sshd 容器**（复用 `internal/integration` 的 `sshd.Dockerfile` 模式），经 SSH 真连真执行 | 内存假服务器（那是单测层级，internal/term/executor 已覆盖） |
| V3 | 驱动入口 | **headless 二进制**（`cmd/gengar-headless`，`--data/--secrets/--pin-all/--yes`）+ `ANTHROPIC_BASE_URL` 指桩、`ANTHROPIC_API_KEY` 注入桩 key | 进程内调用（失去「整装产品」真实性） |
| V4 | 旧套件 | **删除**：e2e_test/runner/sandbox/observer/报告链/旧 YAML/镜像与模板/cmd/e2e-report 一并移除；断言类型概念与 YAML 用例形态移植到新 runner | 保留改造（旧 harness 的输入是本地沙箱+旧事件流，改造成本高于重写） |
| V5 | 断言数据源 | **stdout（plain sink 输出）+ `audit.jsonl`（工具名/Hosts/IsError）** + 跨进程文件（记忆）；不复活 HTML 报告 | 旧 observer 事件流（格式已不存在） |

### 2.2 新套件结构（`internal/e2e`，`go:build e2e`）

```
internal/e2e/
├── SPEC.md
├── README.md            # 运行说明（含 Docker 前置）
├── stubllm.go           # Anthropic Messages SSE 桩：脚本化回合（工具调用序列 → 最终文本）
├── stubllm_test.go      # 桩的 SSE 帧发射单测（不起 Docker，普通 go test 可跑——不加 e2e tag？加 tag 保持包一致，单测同 tag 下运行）
├── sshd.go              # Docker sshd 容器夹具（构建/启停/端口映射；无 Docker Skip）
├── harness.go           # 数据目录播种（host.OpenStore 写主机 + tags）、headless 进程启动/喂输入/收 stdout/audit 读取
├── cases.go             # YAML 用例加载与断言求值（tool_called/tool_not_called/response_contains/response_not_contains/audit_hosts）
├── e2e_test.go          # 用例驱动入口
└── testcases/           # E01-E05（桩）+ R01（真机，env 双门）
```

- **桩协议**：按 `internal/claude/stream.go` 的 parseSSE 所识事件逐帧发射（message_start → content_block_start tool_use → input_json_delta → content_block_stop → … → message_delta stop_reason → message_stop）；脚本第 N 回合对应第 N 个响应；工具结果回传后推进
- **用例**（从旧 T01-T39 精选移植到多主机语义）：
  - **E01** 单机系统信息（system_info 调用 + 回复关键词）
  - **E02** 双机 fan-out（hosts:["all"] → audit 单条 Hosts=2）
  - **E03** 安全黑名单（rm -rf / 被门控拒绝，stdout 含「禁止」）
  - **E04** 标签定向（hosts:["web"] → audit Hosts 恰为两台 web 主机）
  - **E05** 记忆跨会话（第一次 memory_write，重启进程后 memory_read 命中）
  - **R01** 真机冒烟（系统信息；`ANTHROPIC_API_KEY` 未设则 Skip）
- **Docker 不可用 → 全部 Skip**（延续 integration 的约定）；本机当前即此状态，本地验证 = 编译 + 桩单测 + Skip 路径

### 2.3 明确不做（YAGNI）

- HTML/JSON 报告链与 cmd/e2e-report
- GUI 层 E2E（总原则不变）
- 旧 T01-T39 全量移植（精选 6 个代表场景）
- 远端终态断言（docker exec 检查文件等）——stdout/audit 已覆盖本期场景，需要时再加

## 3. README 更新设计

### 3.1 原则

- 以 gengar-pro 桌面端为主体重写；单机 CLI 内容仅保留仍然真实的部分（Agent 能力层：Plugin/安全/ask_user/Skill/Profile/Memory 均存活于桌面端，更新为多主机语义）
- 删除已失真内容：CLI 使用示例、批量模式、TUI 快捷键、`cmd/gengar` 构建/交叉编译、旧 e2e 入口、Alpha 公告（改为当前状态一句话）
- 项目结构对齐当前树（term/xshell/folders/timing/前端组件/brand）；测试命令补全（go/integration/e2e 三 tag + 前端 vitest + wails build）

### 3.2 新结构（章节）

1. 标题 + 一句话定位（桌面多服务器运维 AI Agent）+ 当前能力速览（对话指挥/内嵌终端/XShell 导入/目录树/计时反馈/耿鬼紫夜主题）
2. 构建与运行（wails build/dev、依赖）
3. 使用指南（首次使用/自然语言目标/确认弹窗/终端/导入/目录树——收敛自现有 Alpha 指南并更新）
4. 架构图（桌面端版）+ Agent 能力层（Plugin 表加 hosts fan-out 语义列注、安全分级/批量确认、ask_user、Skill、Profile、Memory 按主机分域）
5. headless 入口与测试（单元/integration/e2e 三个 tag、前端 vitest、E2E 的 Docker+桩说明）
6. 数据目录与环境变量（现状）
7. 项目结构（当前树）
8. 已知限制（更新后的真实清单）

## 4. 验证

- E2E：`go vet -tags e2e ./... && go test -tags e2e ./internal/e2e/ -v`（本机 Docker 缺失 → 用例 Skip、桩单测通过；Docker 可用时全绿）
- README：事实逐条对照代码抽查（构建命令实测、结构树与 ls 对照、能力项与实现对照）
- 全量门：`go build ./... && go test ./...` + 前端双门不受影响

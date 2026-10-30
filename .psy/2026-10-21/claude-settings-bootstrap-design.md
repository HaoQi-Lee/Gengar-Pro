# 初始化配置：从 ~/.claude/settings.json 引导 API 配置 — 设计

日期：2026-10-21
状态：已批准（方案 A）

## 背景与目标

gengar-pro 的 API 配置（`APIKey` / `APIBaseURL` / `Model`）目前只有两个来源：
环境变量（`ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL` / `ANTHROPIC_MODEL`）打底，
SQLite settings 表（设置页写入）覆盖。新用户首次启动时三者为空，必须手动到
设置页填写。而这些信息在机器上往往已经存在——`~/.claude/settings.json` 的
`env` 对象里（Claude Code 的配置方式）。

目标：启动时若发现某字段既无环境变量也无 SQLite 记录，自动从
`~/.claude/settings.json` 解析并**一次性初始化**（写入 SQLite）。

## 字段映射

`ParseClaudeSettings` 从 `env` 对象取值（值为空串视为未设置）：

| gengar 字段 | 取值优先级 |
|---|---|
| `APIKey` | `ANTHROPIC_API_KEY` → `ANTHROPIC_AUTH_TOKEN` |
| `APIBaseURL` | `ANTHROPIC_BASE_URL` |
| `Model` | `ANTHROPIC_MODEL` → `ANTHROPIC_DEFAULT_SONNET_MODEL` → `ANTHROPIC_DEFAULT_HAIKU_MODEL` → `ANTHROPIC_DEFAULT_OPUS_MODEL` |

说明：

- `ANTHROPIC_AUTH_TOKEN` 在 Claude Code 语义中经 `Authorization: Bearer` 发送，
  gengar 客户端只发 `x-api-key`；实测目标网关两种头均接受（认证探测返回
  400-模型无效而非 401），故直接导入无需改客户端。
- MODEL 的三档默认模型回退为确定性顺序（SONNET 优先，与 gengar 内置默认的
  Sonnet 档对齐）。

## 架构

优先级链（三字段各自独立判断，字段级补齐）：

```
SQLite settings（用户显式保存） > 环境变量 > ~/.claude/settings.json（导入+落库） > 内置默认
```

组件：

1. **`internal/config/claude.go`** — `ParseClaudeSettings(data []byte) *Config`
   纯函数：解析 JSON 顶层 `env` 对象，返回只含命中字段的 `Config`（未命中
   字段留空）。无 IO。JSON 损坏或无 `env` 键时返回空 `Config`。
2. **`internal/appcore/core.go`** — `LoadConfig` 重排：三字段先置空，依次叠
   环境变量、叠 SQLite；仍为空的字段尝试从 claude settings 补齐（命中则
   `SetSetting` 落库并填入 cfg），最后回落 `Default()`。`LoadConfig` 用
   `os.UserHomeDir()` 拼出 `~/.claude/settings.json` 路径后交给内部小函数
   `bootstrapFromClaude(store, cfg, path)`——它负责读文件、调
   `ParseClaudeSettings`、落库（签名不变，路径参数即为测试注入口）。
   `MaxSteps` / `CommandTimeout` 不受影响。

## 数据流与触发时机

- **桌面**：`Startup` → `LoadConfig` → 首次导入落库 → `GetSettings` 正常显示
  （APIKey 经 `maskKey` 掩码）→ `rebuildAgent` 直接可用。前端零改动。
- **headless**：`main` → `LoadConfig` 同样受益；原「未配置 API Key」退出路径
  在 claude settings 有 token 时不再触发。
- 第二次启动起 SQLite 已有值 → 导入层短路；`~/.claude/settings.json` 后续
  变化不覆盖已初始化配置（「初始化」语义）。

## 错误处理

- settings.json 不存在 / JSON 损坏 / 无 `env` 键 / 值为空串：**静默跳过**，
  行为与现状一致，不阻断启动、不报错。
- `SetSetting` 落库失败：内存值仍生效，不返回错误（下次启动重试导入）。
- 安全：token 由 settings.json（明文）复制到 `~/.gengar-pro/gengar.db`
  （明文 SQLite）——同为用户主目录内明文，风险面不变。

## 测试

- `internal/config/claude_test.go`（表驱动）：
  - `ANTHROPIC_API_KEY` 优先于 `ANTHROPIC_AUTH_TOKEN`；
  - MODEL 按 SONNET→HAIKU→OPUS 回退；
  - 坏 JSON / 无 `env` / 空值 → 对应字段为空。
- `internal/appcore/core_test.go` 增测（临时目录伪造 settings.json + 测试
  store，直接调 `bootstrapFromClaude`）：
  - SQLite/env 均缺失 → 导入并落库（重开 store 验证持久化）；
  - SQLite 已有值 → 不覆盖；
  - env 已有值 → 不导入该字段。
- 不覆写 `HOME`/`USERPROFILE` 环境变量（Windows 并行测试风险），可测性经
  `bootstrapFromClaude` 的 path 参数注入。

## 范围外

- 设置页清空配置的能力（现状 `SaveSettings` 空值即跳过，维持不变）。
- 客户端认证头改动（`x-api-key` 保持）。
- `~/.claude.json` 等其他 Claude Code 配置文件。

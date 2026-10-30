---
psy_kind: factual
psy_version: 1
package: internal/config
---

# internal/config

## 概述

`internal/config` 包负责为整个 gengar 项目集中管理配置。它定义了统一的 `Config` 结构体，提供合理的默认值，支持通过环境变量覆盖各项配置，并能解析 Claude Code 的 `~/.claude/settings.json`（供启动引导使用）。该包不依赖任何内部包或第三方库，仅使用 Go 标准库。

## 文件

- `config.go` — 定义配置结构体、默认值、显式环境变量读取（`LoadEnv`）与叠加式加载（`Load`）
- `claude.go` — `ParseClaudeSettings`：解析 `~/.claude/settings.json` 顶层 `env` 对象为配置候选值
- `config_test.go` — 默认值、环境变量覆盖、无环境变量回退、`LoadEnv` 显式性四个场景的单元测试
- `claude_test.go` — `ParseClaudeSettings` 表驱动测试（字段映射优先级、回退链、退化输入）

## API

### 类型

- **`Config`** — 项目全局配置容器（APIKey、APIBaseURL、Model、MaxSteps、CommandTimeout）

### 函数

- `Default() *Config` — 返回填充了合理默认值的配置实例
- `Load() *Config` — `Default()` 打底，再用环境变量 `ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_MODEL` 覆盖（当前仅自身测试在用；生产入口走 `appcore.LoadConfig`）
- `LoadEnv() *Config` — 只返回环境变量显式设置的 API 配置，未设置字段留空（无默认值打底）；供需区分「显式配置」与「内置默认」的调用方使用
- `ParseClaudeSettings(data []byte) *Config` — 解析 settings.json 内容，返回仅含命中字段的 `Config`；JSON 损坏或无 `env` 键时返回空 `Config` 而非报错

## 依赖

**标准库：** `os`、`encoding/json`

**第三方库：** 无

**内部包：** 无

## 设计重点

### 零依赖设计

整个包仅依赖 Go 标准库，没有引入任何第三方库或内部包。

### 环境变量优先策略

采用"默认值 + 环境变量覆盖"的十二因子应用模式。`Load()` = `Default()` 打底 + `LoadEnv()` 叠加；`LoadEnv()` 单独存在是为了让 `appcore.LoadConfig` 的优先级链能区分「环境变量显式配置」与「内置默认值」（后者可被 settings.json 引导层填补）。空串环境变量视为未设置。

### settings.json 解析的尽力而为契约

`ParseClaudeSettings` 的字段映射：APIKey ← `ANTHROPIC_API_KEY` → `ANTHROPIC_AUTH_TOKEN`；BaseURL ← `ANTHROPIC_BASE_URL`；Model ← `ANTHROPIC_MODEL` → `ANTHROPIC_DEFAULT_{SONNET,HAIKU,OPUS}_MODEL`（确定性回退顺序）。任何失败（语法错误、无 `env` 键、个别值为非字符串）都不报错而是返回已解码的部分——引导是兜底而非关键路径，失败应保持现状而非阻断启动。

### 不导出可变全局状态

包内没有全局变量或 `init()` 函数，配置实例由调用方显式通过 `Load()` 或 `Default()` 获取。

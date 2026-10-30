---
psy_kind: factual
psy_version: 1
package: internal/skill
---

# internal/skill

## 概述

`internal/skill` 实现 Gengar 的第三种扩展点——Skill（过程性 Playbook）。与提供工具的 Plugin、注入静态事实的 Consensus 不同，Skill 不新增工具，只在被斜杠命令触发时把一段专家 Playbook 作为结构化用户指令注入对话，指导 Agent 如何组合现有工具完成一类任务。

Skill 文件为 Markdown + YAML frontmatter，通过 `go:embed` 在编译期嵌入（`data/*.md`），保持单二进制零外部依赖。每个 Skill 产出（`ToCommand`）一个 `command.Command`，由 `cmd/gengar` 注册进斜杠命令注册表。

## 文件

- `skill.go` — Skill 类型、frontmatter 解析（`parseSkillFile`）、校验（`validate`）、批量加载（`Load`）、命令产出（`ToCommand`）、提示构造（`Prompt`）
- `data/inspect.md` — 内置「服务器巡检」Skill（go:embed 数据，只读）
- `data/install.md` — 内置「软件安装」Skill（go:embed 数据，写操作）

## API

- `Skill` — 结构体：`Name`、`Command`（以 `/` 开头）、`Aliases`、`Description`、`Playbook`
- `Load() ([]Skill, error)` — 读取并解析所有嵌入 Skill；校验字段与 name/command/alias 唯一性
- `(Skill) ToCommand() command.Command` — 转为斜杠命令，触发时打印加载提示并注入 `Prompt()`
- `(Skill) Prompt() string` — 返回注入 Agent 的用户指令文本

## 依赖

**内部包：**

| 内部包 | 用途 |
|--------|------|
| `gengar-pro/internal/command` | 产出斜杠命令；通过 `command.AgentLike.ProcessInput` 注入 Playbook |

**第三方库：** `gopkg.in/yaml.v3`（解析 frontmatter）

## 设计重点

### 解耦
`skill` 单向依赖 `command`；`agent` 不 import `skill`。Skill 注入复用 `agent.ProcessInput`，Agent 侧无需感知 Skill 概念。

### 编译期校验
`Load` 在启动期解析全部 Skill，任一格式/字段/唯一性错误即返回错误，由 `main` 转为 `os.Exit(1)`——嵌入数据损坏不静默启动（与 `consensus` 行为一致）。

### Playbook 即指令
`Prompt()` 只提供通用框架文本（`[Skill]` 标记 + 「按 Playbook 执行」+ Markdown 汇总输出格式），不再绑定具体操作约束——只读/写操作、选用哪些工具由各 Playbook 自带（如 `inspect.md` 自带只读约束，`install.md` 自带写操作确认流程）。`ToCommand()` 触发时打印加载提示，并把 `ctx.Args`（斜杠命令参数，如 `/install nginx` 的 `nginx`）追加进注入文本，复用现有 ReAct 循环与安全门控。

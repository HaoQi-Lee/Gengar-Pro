---
psy_kind: factual
psy_version: 1
package: internal/command
---

# internal/command

## 概述

`internal/command` 提供斜杠命令注册表（Registry）：Skill 命令（`/inspect`、`/install` 等）经此注册，由 Web 对话与 headless 两条入口的 dispatcher 统一路由（前缀拆参 → Lookup → Run）。早期的内置命令（`/help`、`/quit`、`/clear`、`/history`）随 CLI 形态退役移除——Web 端退出即关页、清空会话由设置重建 Agent 承担。

## 文件

- `command.go` — 核心类型（Command、Registry、Context、AgentLike）与注册表方法（All、Match、Lookup）、`SplitArgs` 拆参
- `command_test.go` — 注册表与拆参的单元测试，含 `fakeAgent` 测试替身

## API

### 类型

- **`AgentLike`** (interface) — Agent 表面子集接口，仅 `ProcessInput(ctx, input) string`（Skill 命令注入 Playbook 驱动 ReAct 循环）
- **`Context`** — 命令执行依赖注入容器（Agent + io.Writer + `Ctx context.Context` + `Args []string`）；`Args` 由 dispatcher 透传斜杠命令参数（如 `/install nginx` 的 `nginx`）
- **`Command`** — 斜杠命令定义（Name、Aliases、Description、Run 闭包）；Run 无返回值（`/quit` 的 REPL 终止语义已随内置命令移除）
- **`Registry`** — 命令注册表，持有固定的命令切片

### 函数/方法

- `NewRegistry(cmds ...Command) *Registry` — 构建新 Registry（防御性拷贝）
- `(r *Registry) All() []Command` — 返回所有命令（副本）
- `(r *Registry) Match(prefix string) []Command` — 按前缀匹配命令（须以 `/` 开头，结果按字典序排列）
- `(r *Registry) Lookup(input string) *Command` — 按精确名称或别名查找
- `SplitArgs(line string) (cmd string, args []string)` — 把一行斜杠输入拆成命令名与参数（空白分隔，不做 shell 解析；单 token 或空输入时 args 为 nil），供 dispatcher 统一解析

## 依赖

**标准库：** `context`、`io`、`sort`、`strings`

**第三方库：** 无

**内部包：** 无（通过自定义 `AgentLike` 接口实现依赖倒置）

## 设计重点

### 依赖倒置

包内定义 `AgentLike` 接口而非直接导入 agent 包，降低循环依赖风险和耦合度。

### 命令即数据

`Command` 将名称、别名、描述、执行函数统一封装，注册表不区分命令来源（当前全部来自 Skill 的 `ToCommand`），易于扩展。

### Match 与 Lookup 分离

`Match` 用于前缀模糊匹配（自动补全），`Lookup` 用于精确匹配（命令执行），职责清晰分离。

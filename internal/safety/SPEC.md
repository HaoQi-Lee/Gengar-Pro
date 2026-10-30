---
psy_kind: factual
psy_version: 1
package: internal/safety
---

# internal/safety

## 概述

`internal/safety` 包是一个**命令与工具安全分类引擎**，负责在 AI Agent 自动执行操作之前判断其风险等级。采用二元模型（Safe / Risky）：将只读、无副作用的操作标记为 `LevelSafe`（可自动执行），将所有不可证明为安全的操作统一标记为 `LevelRisky`（需用户确认）。同时支持静态工具名分类和动态 shell 命令内容分析，通过多组正则表达式对命令进行白名单、黑名单和危险操作匹配。

## 文件

- `gate.go` — 核心门控结构体 `Gate`，提供按工具名和命令内容进行安全分类的方法
- `level.go` — 定义安全等级类型 `Level`（Safe/Risky）及静态工具名到安全等级的映射表
- `rules.go` — 定义三组正则表达式（黑名单、白名单、危险操作）及受保护路径列表
- `gate_test.go` — Gate 的单元测试，覆盖白名单、高危、误报防御、默认兜底等场景
- `level_test.go` — Level.String() 和 classifyStatic 的单元测试
- `config_test.go` — 高危命令和受保护路径的补充测试

## API

### 类型

- **`Level`** — 安全等级枚举（`LevelSafe`=0 / `LevelRisky`=1），实现 `fmt.Stringer`

### 结构体

- **`Gate`** — 安全分类门控器，无内部状态

### 函数/方法

- `NewGate() *Gate` — 创建 Gate 实例
- `(g *Gate) Classify(toolName, input) Level` — 统一安全分类入口，自动路由静态/动态路径
- `(g *Gate) ClassifyStatic(toolName) Level` — 仅依据工具名查静态映射表
- `(g *Gate) ClassifyCommand(cmd) Level` — 对 shell 命令动态分类：拆段（`;` `|` `&&` `||`）逐段黑名单→逐段白名单全过才 Safe；命令替换/重定向直接 Risky
- `(g *Gate) MatchHighRisk(cmd) bool` — 判断是否匹配高危规则，供 UI 标注
- `(g *Gate) SetStaticLevel(toolName, level)` — 注册工具静态分级（plugin.Registry.SetGate 灌入插件声明，单一真相源）

### 常量

- `LevelSafe` — 安全：只读/无副作用，可自动执行
- `LevelRisky` — 风险：有副作用或未知，需用户确认

## 依赖

**标准库：** `encoding/json`、`fmt`、`regexp`、`strings`

**第三方库：** 无

**内部包：** 无（plugin 包依赖 safety 定义分级——静态分级表由 plugin.Registry.SetGate 启动时灌入，方向不可反转，safety 不得 import plugin）

## 设计重点

### 默认拒绝（Default-Deny）

所有未在白名单或静态映射表中明确标记为 Safe 的工具和命令，一律回退到 `LevelRisky`，体现最小权限原则。

### 分段判定（2026-10 评审 ①-3）

`ClassifyCommand` 把命令按 `;` `|` `&&` `||` 拆段，**每段**都过白名单整串才 Safe；含命令替换（`$(...)` / 反引号）或重定向（`>` / `>>` / `<` / heredoc）直接 Risky。旧实现白名单 `^cmd\b` 只锚串首，`cat x; rm -rf /data` 命中 `^cat` 即 Safe 免确认——分段后任一段未命中即 Risky。白名单同时移除 `env`（泄密面）、`curl`/`wget`（外带/管道执行面）。引号内分隔符同样拆段（不做引号解析）：误拆只多弹确认，绝不放行。

### 命令正则的分层匹配

黑名单优先（高危）→ 白名单逐段全过（只读，段首锚定）→ 兜底 Risky。黑名单正则感知 `;`、`|`、`||`、`&&` 以及 `sudo`/`doas` 前缀，确保 `ls && reboot` 类复合命令中的危险部分被捕获。`dangerousRegexps` 不再驱动分级（分段判定下破坏性段必然不过白名单），保留供 UI/未来分级参考。

### 误报防护

白名单段首锚定设计确保 `echo "reboot"`、`cat /var/log/reboot.log` 等安全命令不会被错误拦截；链式只读组合（`df -h; free -m`、`ps aux|grep nginx`）仍免确认。

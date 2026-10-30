---
psy_kind: factual
psy_version: 1
package: internal/consensus
---

# internal/consensus

## 概述

`internal/consensus` 包负责按环境 profile（如 `dev`、`prod`）加载和管理"环境共识数据"。这些共识数据描述了不同环境下的基础设施事实（如 YUM 源地址、NTP 服务器、HTTP 代理）和运维规范（如配置文件备份策略）。数据以 YAML 格式存储，通过 `go:embed` 在编译期嵌入二进制文件，运行时根据环境变量（`GENGAR_ENV`，默认 `dev`）选择对应的 profile 进行加载、校验和查询。

## 文件

- `consensus.go` — 核心实现：定义 Consensus / Guideline 类型，提供 Load、validate、Find / Catalog / IDs 等函数
- `consensus_test.go` — 单元测试：覆盖默认/显式 profile 加载、未知环境错误、ID 唯一性等场景
- `data/dev.yaml` — 开发环境共识数据（http-proxy、yum-repo、ntp-servers、backup-policy）
- `data/prod.yaml` — 生产环境共识数据（yum-repo、ntp-servers、backup-policy）

## API

### 常量

- `DefaultEnv` — 默认 profile 名称 `"dev"`

### 类型

- **`Consensus`** — 一个 profile 的完整环境共识（Profile、Description、Guidelines 列表）
- **`Guideline`** — 单条共识条目（ID、Title、Summary、Content）

### 函数与方法

- `Load(env string) (*Consensus, error)` — 加载对应 profile 的 YAML 数据
- `(c *Consensus) Catalog() string` — 生成紧凑的共识目录文本，用于注入 Agent 系统提示
- `(c *Consensus) Find(id string) *Guideline` — 按 ID 精确查找
- `(c *Consensus) IDs() []string` — 返回所有 guideline ID 列表

## 依赖

**标准库：** `embed`、`fmt`、`sort`、`strings`

**第三方库：** `gopkg.in/yaml.v3`

**内部包：** 无

## 设计重点

### 编译期嵌入

通过 `go:embed` 将 YAML 数据文件在编译时嵌入二进制，部署时无需携带额外数据文件。

### Profile 校验双重保障

`validate` 要求 YAML 内的 `profile` 字段必须与文件名一致，所有 guideline 的 id/title/summary/content 均不得为空，且 ID 不可重复。

### 面向 Agent 的目录机制

`Catalog()` 生成面向 AI Agent 的中文提示文本，明确告知 Agent 应通过 `query_consensus(topic=<id>)` 查询详情，不凭直觉编造。

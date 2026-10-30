---
psy_kind: factual
psy_version: 1
package: cmd/gengar-headless
---

# cmd/gengar-headless

## 概述

`cmd/gengar-headless` 是 gengar-pro 的无 GUI 验证入口（测试设施，非产品）：stdin 逐行驱动 Agent 循环，plain Sink 输出，斜杠命令与 Skill 全保留——让集成测试与自动化脚本不启动桌面应用即可验证产品逻辑。

## 文件

- `main.go` — 入口全部实现：flag 解析、数据目录准备、组装（appcore + audit + command + skill）、stdin 循环、信号处理

## API

- `main()` — 唯一入口。命令行参数：
  - `--data <dir>` — 数据目录（清单/记忆/审计/known_hosts，默认 `~/.gengar-pro`）
  - `--yes` — 自动批准全部 Risky 确认（设 `ui.ConfirmAutoApprove`）
  - `--pin-all` — 把清单内全部主机设为默认目标
  - `--secrets <ref=pass,...>` — 内联秘密注入内存 SecretStore（默认取 `GENGAR_SECRETS`；值不能含逗号；不提供时为空内存实现——测试设施不碰任何真实凭据存储）
- 环境变量：`GENGAR_SECRETS`（同 `--secrets`）、`GENGAR_COMMAND_TIMEOUT`（命令超时，默认 30s）、`GENGAR_ENV`（共识 Profile）、`ANTHROPIC_API_KEY` 等 API 配置

退出码：数据目录/共识/API Key 不可用、或 stdin 读取失败（ErrTooLong 截断、IO 错误）时非零退出——截断静默当通过会污染 CI。

## 依赖

**内部包：** `appcore`、`audit`、`command`、`consensus`、`executor`（known_hosts 重定向）、`host`、`skill`、`ui`、`ui/plain`

**第三方库：** 无直接依赖

**标准库：** `bufio`、`context`、`flag`、`fmt`、`os`、`os/signal`、`path/filepath`、`strings`、`syscall`、`time`

## 设计重点

### 入口负责建父目录

`appcore.OpenAt` 不建父目录；headless 面向一次性数据目录（CI / 集成测试），由入口 `MkdirAll` 后再打开，空目录直接可用。

### 秘密与 TOFU 全部随数据目录隔离

默认 SecretStore 是空 `MemoryStore`（`ParseEnvSecrets("")`），仅 `--secrets`/`GENGAR_SECRETS` 注入内联秘密——不碰任何真实凭据存储；known_hosts 在 OpenAt 前重定向到 `<data>/known_hosts`（与 gengar-server 入口语义一致），测试服务器的临时主机密钥不污染全局 TOFU 库。

### 共识加载失败即退出

`BuildAgent` 对 nil Consensus 有防护，但 nil 时系统提示词缺共识目录、query_consensus 恒报错——入口对 `consensus.Load` 失败直接退出而非降级。

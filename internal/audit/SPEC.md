---
psy_kind: factual
psy_version: 1
package: internal/audit
---

# internal/audit

## 概述

`internal/audit` 记录 gengar-pro 的操作审计流水：谁（会话）在何时对哪台主机执行了什么工具、是否经过确认、结果如何。JSONL 追加写，每行一个完整 JSON 对象，方便 jq / grep 事后分析。按天分片轮转（`audit-YYYY-MM-DD.jsonl`，保留 30 天，启动与跨天首写时清理过期分片）；写失败计数暴露给 `/api/health` 作运维告警信号。

## 文件

- `audit.go` — `Entry` 结构体与 `Logger`（按天分片 JSONL 追加写，并发安全；轮转/清理/写失败计数）
- `audit_test.go` — 单元测试（含轮转清理与分片路径）
- `audit_user_test.go` — User 字段与 omitempty 行为测试

## API

- `type Entry struct` — 一条审计记录：`TS`（RFC3339）、`Session`、`User`（web 多用户下的操作者标识；headless 单机场景为空，omitempty 时不出现在 JSON，旧流水解析不受影响）、`Tool`、`Input`（原始 JSON，nil 序列化为 null）、`Hosts`（实际生效的主机名）、`Approved`、`IsError`、`Output`
- `Open(path string) (*Logger, error)` — 打开轮转式审计。path 为历史固定路径（如 `<data>/audit.jsonl`）：新分片写 `<dir>/audit-<date>.jsonl`；目录自动创建，分片权限 0o600
- `(l *Logger) Log(e Entry) error` — 序列化并追加一条记录，立即 Flush 并 fsync；跨天首写自动轮转分片；任何写失败累加计数（`WriteFails`）
- `(l *Logger) WriteFails() int` — 累计写失败次数（`/api/health` 暴露，>0 = 磁盘/权限问题）
- `(l *Logger) CurrentShard() string` — 当前分片路径（health/排错用）
- `(l *Logger) Close() error` — Flush 并关闭文件

## 依赖

**内部包：** 无（被 `agent` 与两条入口消费；e2e harness 读分片或旧固定名）

**第三方库：** 无

**标准库：** `bufio`、`encoding/json`、`fmt`、`os`、`path/filepath`、`sort`、`strings`、`sync`、`time`

## 设计重点

### 宁可慢不可丢

每条记录立即 `Flush` + `Sync` 落盘，不做缓冲批量写——审计流的价值在崩溃/断电后仍完整，吞吐不是目标。写失败累加计数并返回错误（调用方降级为日志事件），计数暴露到 health 端点。

### 按天分片轮转（评审 ③-3）

单文件无限增长的 audit.jsonl 在长期运行下既难检索也难清理。分片文件名 `audit-YYYY-MM-DD.jsonl` 字典序即时间序；跨天首写触发 `rotateLocked`（关旧开新 + `pruneLocked` 清理超 30 天分片）。历史固定名文件不再续写但保留（旧流水仍可读）。消费方（e2e harness、agent 测试）按 glob 分片读取，旧固定名兜底。

### 权限收紧

0o600 属主可读：审计记录含命令输入，可能带秘密，不给同机其他账户读的必要。

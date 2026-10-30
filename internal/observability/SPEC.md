---
psy_kind: factual
psy_version: 1
package: internal/observability
---

# internal/observability

## 概述

`internal/observability` 包为 Gengar 项目的端到端（E2E）测试系统提供结构化事件日志能力。它定义了统一的 `Logger` 接口和 `Event` 数据结构，通过三种实现（空操作日志器、内存日志器、文件日志器）将 Agent 执行过程中的关键事件以 JSONL 格式持久化，供测试运行后的分析与回溯使用。

## 文件

- `e2elog.go` — 定义核心类型 Event、Logger 接口及三种实现（Noop、MemoryLogger、FileLogger）
- `e2elog_test.go` — 针对三种日志器的单元测试，覆盖基本功能、JSONL 格式及并发安全性

## API

### 类型

- **`Event`** — 结构化事件（时间戳、事件类型、步骤编号、工具名称/ID、输入输出、错误信息等）
- **`Logger`** (interface) — 日志器接口，`Log(Event)` + `Close() error`

### 函数

- `Noop() Logger` — 返回丢弃所有事件的空日志器
- `NewMemoryLogger() *MemoryLogger` — 创建内存日志器，用于单元测试
- `NewFileLogger(path string) (Logger, error)` — 创建 JSONL 文件日志器，用于 E2E 测试

### 方法

- `(*MemoryLogger) Events() []Event` — 返回已记录的所有事件的副本（线程安全）

## 依赖

**标准库：** `bufio`、`encoding/json`、`os`、`sync`

**第三方库：** 无

**内部包：** 无

## 设计重点

### 接口驱动，策略分离

通过 `Logger` 接口抽象，三种实现分别服务于不同场景（生产丢弃、单元测试内存缓冲、E2E 文件持久化）。

### JSONL 格式

选择 JSON Lines 格式，便于流式写入和后续逐行解析，无需一次性加载整个日志文件。

### 并发安全保证

MemoryLogger 和 FileLogger 均使用 `sync.Mutex` 保护共享状态，`Events()` 返回深拷贝。

### json.RawMessage 延迟解析

`Event.Input` 使用 `json.RawMessage`，保留原始 JSON 字节，避免不必要的反序列化开销。

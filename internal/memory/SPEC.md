---
psy_kind: factual
psy_version: 1
package: internal/memory
---

# internal/memory

## 概述

`internal/memory` 包为 Gengar 项目提供服务器记忆持久化能力（按主机分域）。数据以 JSON 文件存于 `~/.gengar-pro/memory/<host>.json`，每个受管主机一个文件，包含两个预定义分类：`system_info`（服务器基本信息）和 `history`（Agent 执行过的关键操作记录）。`Store` 按主机分发 `Memory` 实例并负责文件路径清洗，`Memory` 提供单主机的读写删与落盘。

## 文件

- `memory.go` — 包的全部实现代码：`Store`（按主机分域 + 路径清洗）、`Memory`/`Category` 类型及 Save/Write/Read/Delete 方法
- `memory_test.go` — 单元测试，覆盖 Get/Write/Read/Delete、路径清洗、Windows 设备别名等场景
- `store_test.go` — Store 跨包测试辅助（`OpenStoreForTest`）的用法测试

## API

### 类型

- **`Store`** — 按主机分域管理记忆文件 `<dir>/<host>.json`；host 键为主机名（清单里 Name 唯一）
- **`Memory`** — 单个主机的完整记忆数据（`SystemInfo`/`History` 两个 `*Category` 字段 + Store 注入的落盘路径）
- **`Category`** — 单个分类结构体（`UpdatedAt time.Time` + `Data interface{}`，保留 JSON 原始结构）

### 函数/方法

- `OpenStore() (*Store, error)` — 打开默认记忆目录 `~/.gengar-pro/memory`
- `OpenStoreDir(dir string) (*Store, error)` — 指定数据目录（测试与 appcore 用），目录不存在则创建
- `OpenStoreForTest(dir string) *Store` — 跨包测试构造辅助（忽略错误）
- `(s *Store) Dir() string` — 返回记忆数据目录
- `(s *Store) Get(host string) *Memory` — 返回主机记忆；文件不存在或损坏时返回空 Memory（不视为错误）。同主机多次 Get 返回独立实例
- `(m *Memory) Save() error` — 写回 Store 注入的路径（缩进 JSON，0644）；直接构造、无路径的 Memory 报错
- `(m *Memory) Write(category, data) error` — 写入指定分类，自动设 UTC 时间戳
- `(m *Memory) Read(category) (interface{}, error)` — 读取指定分类；`"all"` 返回全部（空 Memory 时返回空 map）
- `(m *Memory) Delete(category) error` — 删除指定分类；`"all"` 同时删除磁盘上的 `<host>.json`（文件不存在不报错）

## 依赖

**标准库：** `encoding/json`、`fmt`、`os`、`path/filepath`、`strings`、`sync`、`time`

**第三方库：** 无

**内部包：** 无（被 `plugin`/`appcore` 消费）

## 设计重点

### 文件即数据库，按主机分域

每个受管主机一个 JSON 文件，目录即索引，降低部署和运维复杂度。重命名主机视为新机器从零记忆，旧文件保留不迁移（alpha 明确接受）。

### Get 返回独立实例（读改写语义）

`Get` 每次从磁盘新读：同主机连续两轮写入不共享内存态，每轮 Get→Write→Save 是一次完整的读改写，并发下 last-write-wins——对 read→append→write 的用法够用，避免跨轮持有陈旧引用。

### 主机名清洗与 Windows 设备别名

`hostPath` 只允许字母数字与 `- _ .`，其余替换为 `_`（防目录穿越）；清洗结果命中 Windows 设备别名（CON、NUL、COM1… 按首个 `.` 前的词、不区分大小写匹配）时加 `_` 前缀——`CON.json` 在 Windows 10 及更早会静默写到设备而不落盘，记忆凭空丢失。规则跨平台统一，文件名可预期。

### 分类固定、白名单校验

仅允许 `system_info` 和 `history` 两个分类，未知分类立即返回错误，避免数据膨胀。

### 优雅的空状态处理

`Get` 文件不存在或损坏时返回空 Memory；`Delete("all")` 文件不存在时静默成功，无需额外初始化。

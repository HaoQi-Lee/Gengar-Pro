# 用户管理与主机按用户隔离 — 设计文档

日期：2026-10-30
状态：已批准（用户逐节确认）

## 1. 目标

为 admin 的用户管理模块补齐「主机清单按用户隔离」：

- 每个登录用户只能看到、操作自己名下的主机与主机目录（folder 随主机走，天然隔离）
- admin 登录后看到全部主机，可操作任何主机
- 存量主机升级后归属 admin
- 删除用户时其名下主机自动移交 admin（数据不丢）

用户管理的创建/重置密码/删除功能已存在（`internal/webapi/users.go` + `frontend/src/components/UsersDialog.tsx`），本设计不新增划转功能。

## 2. 方案选择（已定：方案 A）

**`hosts.owner` 列 + 查询层按属主过滤**。

否决的替代方案：

- 方案 B（每用户一张 hosts 表）：动态建表/迁移 ×N、审计跨表查询复杂，与「单库 + 幂等 ALTER」惯例冲突
- 方案 C（webapi 内存过滤，不加列）：appcore.Session 的 Agent 目标解析层拿不到 username 上下文，改法更绕

## 3. 数据模型与迁移（internal/host）

### 3.1 schema 变更

`hosts` 表新增 `owner TEXT NOT NULL DEFAULT ''`：

- `store.go` 的 `const schema` 建表语句加列（新库直接带列）
- `OpenStore` 增加幂等 `ALTER TABLE hosts ADD COLUMN owner TEXT NOT NULL DEFAULT ''`——报 `duplicate column` 则忽略，其他错误致命（与 folder / users.role 迁移同惯例）
- `Host` 结构体加 `Owner string` 字段（`host.go`）

### 3.2 空 owner 的语义

空 `owner` = 归 `admin`。**不在迁移时刷数据**，读侧归一（`ListFor` 中 `owner = ? OR owner = ''` 判 admin 归属）。升级零风险，语义集中一处。

### 3.3 Store 新增/调整方法

- `ListFor(owner string, isAdmin bool) ([]*Host, error)` — 普通用户返回 `owner = ?` 的行（ORDER BY name 不变）；admin 返回全表。空 owner 行在 admin 视角下可见（归一语义）
- `ReassignHosts(from, to string) error` — `UPDATE hosts SET owner = ? WHERE owner = ?`；删除用户时移交主机给 admin 用
- `Save`/`Delete`/`List` 保留原语义（`List` 仍返回全表——Core/headless 全局路径不变；owner 由 webapi 层在 Save 前赋值）

## 4. webapi 层隔离（internal/webapi）

### 4.1 读侧

- `handleGetHosts`：`List()` → `ListFor(username, isAdmin)`。需要一次 `GetUser` 查角色（每请求查库，与 `admined` 同代价模式）
- `findHost(id)` → `findHostFor(id, username, isAdmin)`：找不到**或非属主且非 admin**统一返回 nil → 对外 404「主机不存在」（不区分两者，与 `chatForUser` 防存在性探测语义一致）

经 `findHost` 的全部入口自动获得隔离：编辑（saveHost 防复活复查）、删除、连通测试、**终端开启**（bob 对 alice 主机开终端 → 404）。

### 4.2 写侧

- `saveHost`：新建时 `h.Owner = username`；编辑路径经 `findHostFor` 校验属主（非属主/非 admin → 400「主机已被删除」沿既有文案，不泄漏存在性）。`handleSaveHost`/`saveHost` 签名加 username/isAdmin 参数
- `handleXshellPreview/Commit`：判重键与重名集合按 `ListFor` 取——导入只判「自己视角内」的重复；`handleXshellCommit` 经 saveHost 自动归属导入者
- `handleTestHostDraft`：req.ID 非空时继承已存主机同样经 `findHostFor`（他人主机 → 404）
- `handleSetScope` / `scopeNames`：`List()` → `ListFor(...)`——目标设定只过滤到自己视角的主机

### 4.3 Session（appcore）

这是最深的一层：Agent 的 CatalogText / Names / Resolve 经 Session 的 `inventory()` 读全表。

- `Core.NewSession()` → 新增 `NewSessionFor(owner string, isAdmin bool)`（保留 `NewSession()` 兼容既有测试/调用，语义 = 全局视图）；Session 持有 owner 视角，`inventory` 读 `ListFor`
- `chatOpen`/`newChatSession`（webapi/state.go）创建 Session 时传 owner 与角色。角色在 chatSession 创建时查库一次并快照（角色创建后不可变，与 `admined` 每请求查库同一前提）
- Session 的 pinned 集合可能持有划转后不再属于该用户的主机 ID——解析时被 `ListFor` 自然过滤（跳过已删除项的现语义复用），无需额外清理

### 4.4 删除用户时主机移交

`handleDeleteUser` 在吊销 token 之前增加：

```go
if err := s.core.Store().ReassignHosts(name, "admin"); err != nil { ...500... }
```

审计条目 Output 附加移交数：`user=bob hosts=3→admin`（数量经 `ListFor(name, false)` 前置统计）。

前端 UsersDialog 删除确认文案改为「其名下主机将移交 admin」。

## 5. 不改的部分

- 前端 `App.tsx`/`Sidebar.tsx`/`folders.ts`/`ScopePicker.tsx`——hosts state 本就是唯一数据源，`GET /api/hosts` 过滤后前端天然隔离
- `term.Manager` 的 owner 机制——开终端的属主校验在 webapi 层前置（findHostFor），term 层不变
- 审计模块——`Entry.User` 已有，host 操作审计已盖戳
- 路由表——无新端点（隔离是既有端点的行为变更）

## 6. 错误处理

- 非属主访问他人主机 → 统一 404「主机不存在」（REST）/ 400「主机已被删除」（saveHost 编辑路径既有文案），不泄漏存在性
- `ReassignHosts` 失败 → 500「移交主机失败」，用户删除中止（不吊销 token，可重试）
- `ListFor` 失败 → 既有 500 路径

## 7. 测试策略

- `internal/host/store_test.go`：
  - owner 列迁移（旧库无 owner 列 → OpenStore 迁移 → 空 owner 行归一 admin 可见）
  - `ListFor` 过滤（普通用户只见自己的；admin 见全部含空 owner）
  - `ReassignHosts` 往返
- `internal/appcore/session_test.go`：`NewSessionFor` 视角下 CatalogText 不含他人主机；Names/Resolve 解析不到他人主机
- `internal/webapi/isolation_test.go`（新）：
  - alice/bob 各建主机 → alice 列表不见 bob 的；bob 编辑/删除/开终端/草稿测试 alice 主机 → 404
  - admin 列表见全部；admin 可编辑/删除任何主机
  - 删除 bob → bob 主机 owner 变 admin，admin 列表仍全见；alice 不受影响
  - alice/bob 各自 XShell 导入判重互不串扰
  - scope 设定：bob 的会话 scope 不能钉住 alice 的主机
- 前端 vitest：无新交互（组件未改动），不加

## 8. 影响面清单（实现参考）

| 文件 | 变更 |
|---|---|
| `internal/host/store.go` | schema + ALTER + `ListFor` + `ReassignHosts` |
| `internal/host/host.go` | `Host.Owner` 字段 |
| `internal/appcore/session.go` | `NewSessionFor` + inventory 按视角 |
| `internal/webapi/hosts.go` | `handleGetHosts`/`findHostFor`/`saveHost`/`handleSetScope`/`scopeNames`/`handleTestHostDraft` |
| `internal/webapi/xshell.go` | 判重/重名集合按 `ListFor` |
| `internal/webapi/state.go` | `newChatSession` 传 owner/角色 |
| `internal/webapi/users.go` | `handleDeleteUser` 移交 + 审计 |
| `internal/webapi/auth.go` 或公共处 | 视角判定辅助（`userViewOf(username)` 一次查库拿 role） |
| `frontend/src/components/UsersDialog.tsx` | 删除确认文案 |
| 各 `_test.go` | 见 §7 |

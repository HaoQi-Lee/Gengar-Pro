# 内置管理员 + Web 用户管理 设计

日期：2026-10-22 · 分支：feature/web-system

## 背景与目标

gengar-server 目前创建登录用户只有 CLI（`--add-user`，交互输密码），Web 上无法建号，且无角色概念。目标：

1. **内置管理员 admin/admin**：首次启动自动创建，开箱即用（内网 alpha 定位，不做首登强制改密，启动日志提醒）。
2. **Web 用户管理（仅 admin）**：用户列表、创建用户并设初始密码、重置任意用户密码、删除用户。

非目标：角色编辑（创建的用户一律普通角色）、用户自己改密、首登强制改密流程、WS 连接的即时吊销。

## 数据层（internal/host/store.go）

- `users` 表加 `role TEXT NOT NULL DEFAULT ''`：
  - 新库：直接进 `schema` 常量。
  - 旧库：`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT ''`，同 `folder` 列迁移模式（duplicate column 错误视为已迁移，幂等）。
- `User` 结构加 `Role string`；`GetUser`/`CreateUser` 补读写该列。
- 新增方法：
  - `ListUsers() ([]*User, error)` — 按 username 排序。
  - `UpdateUserPassword(username, pwHash string) error` — 不存在返回包装 ErrNoSuchUser。
  - `DeleteUser(username string) error`。
  - `SetUserRole(username, role string) error` — 不存在返回包装 ErrNoSuchUser。

## 种子逻辑（main.go）

启动时（`--add-user` 分支之后、serve 之前）执行 `ensureAdmin(store) error`：

- `admin` 不存在 → `CreateUser(admin, bcrypt("admin"), role=admin, now)`，log「已创建内置管理员 admin/admin，请尽快修改密码」。
- `admin` 已存在 → `SetUserRole(admin, admin)`（旧库 CLI 建的 admin 补提权；重复设值幂等无害）；若 `bcrypt.CompareHashAndPassword(hash, "admin")` 通过 → log 警告「admin 仍在使用默认密码」。

做成独立可测函数 + `main_test.go`（现有 runAddUser 因 log.Fatalf 不可测，不重蹈覆辙）。失败时 main 走 `log.Fatalf`（无管理员则系统不可管理，宁可拒启）。

## API 层（internal/webapi/users.go 新文件）

**`admined` 包裹器**：`authed` 拿到 username 后 `GetUser` 查 role；非 admin 或用户不存在 → 403「需要管理员权限」。每请求查一次库：角色创建后不可变，内网流量下省掉 token 表结构复杂化。

**`/api/me` 扩展**：返回 `{username, role}`（handler 内再 GetUser 取 role）。

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/api/users` | `{users: [{username, role, createdAt}]}` |
| POST | `/api/users` | 创建普通用户 `{username, password}`；重名 → 400「用户名已存在」 |
| PUT | `/api/users/{username}/password` | `{password}` 重置；成功后撤销该用户全部 token（强制重登录） |
| DELETE | `/api/users/{username}` | 删除；不能删自己、不能删最后一个 admin → 403 |

校验：

- 用户名 trim 后匹配 `^[A-Za-z0-9_.@-]{1,32}$`（避免 URL path / sessionID 出怪字符）；密码 6–128 位。
- 密码 bcrypt（`host.PwHashCost`）后落库；任何响应不回显 hash。

删除用户清理（锁内取走、锁外清理，遵循现有锁序 statesMu → 释放 → taskMu）：

1. 撤销该用户全部 token。
2. `statesMu` 内摘除 userState，锁外 `taskMu` 加锁 cancel 在跑任务。
3. 已知限制（alpha 可接受）：已建立的 WS 连接保持到自然断开（token 失效后刷新页面即回落登录页）。

审计：建号 / 重置密码 / 删号各记一条 `audit.Entry`（沿 `logTerm` 模式，`Tool: user_create / user_pw_reset / user_delete`，`User` 为操作者）。

## 前端（frontend/src）

- `client.ts`：`GetUsers` / `CreateUser` / `ResetUserPassword` / `DeleteUser`；`Me()` 返回类型加 `role`。
- `App.tsx`：登录态从 `username: string` 扩为 `me: {username, isAdmin}`（Me 探测与 Login 回调带出；`isAdmin = role === "admin"`）。
- `Sidebar.tsx`：「用户」按钮仅 admin 渲染，busy 时禁用（同设置按钮）。
- `UsersDialog.tsx`（新，套 Settings/HostForm 弹窗模式）：
  - 列表：用户名 / 角色徽标（admin）/ 创建时间 / 行内操作。
  - 创建表单：用户名 + 密码 + 确认密码（前端校验一致性）。
  - 重置密码：行内展开输入框 + 确定。
  - 删除：两次点击确认（同主机删除交互）；当前登录用户行的删除按钮禁用。

## 测试

- `internal/host/store_users_test.go`：role 列迁移（旧 schema 开库补列）、ListUsers 排序、UpdateUserPassword / DeleteUser / SetUserRole 的 ErrNoSuchUser 路径。
- `main_test.go`：ensureAdmin 三分支——新建、已存在补角色、默认密码探测（探测分支不依赖 log 断言，以返回行为准）。
- `internal/webapi/users_test.go`（新）：非 admin 403、未登录 401、建号 + 重名 400、非法用户名/短密码 400、重置密码后旧 token 401、删自己 403、删最后 admin 403、/api/me 带 role。
- 前端：client 层测试对齐 client.test.ts 现状（组件无测试，不新增组件测试）。

## 环境注意

- 本机 `go test -race` 全局损坏——并发正确性用压力跑（循环并发调用）替代（见用户环境备忘）。

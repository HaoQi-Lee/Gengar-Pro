# 多对话会话（按 session 隔离） 设计

日期：2026-10-23 · 分支：feature/web-system

## 背景与目标

现状：服务端状态按用户名聚合——同账号（如 admin）的所有登录共享同一份对话（hub 缓冲、Agent 历史、单任务锁），且前端只有固定一个对话 tab。目标：**每个对话 tab 是独立会话**（可多开），不同登录（哪怕同账号）互相隔离。

## 模型（后端）

拆分 `userState`：

- **按用户名**：`appcore.Session`（钉住/默认目标——账号级偏好，同账号共享，现状语义不变）
- **按会话 ID**（新 `chatSession`）：hub（缓冲/重放）、sink（含 onIdle 取消待决议）、Agent（独立对话历史）、单任务锁；字段 `id`（8 字节随机 hex）、`owner`（创建者用户名）、`agentSessionID`（审计/记忆标识）

**会话创建与绑定（零新增 REST 端点）**：

- `GET /api/ws/chat?since=N&session=ID`：session 为空或未知（含服务重启后的 stale id）→ 服务端新建会话；`hello` 帧新增 `session` 字段告知实际 id——客户端据此绑定/重绑（stale 复活为新会话，天然匹配「服务重启会话清空」已知限制）
- `POST /api/chat`、`/api/chat/cancel`、`/api/confirm/{id}`、`/api/ask/{id}` 请求体加 `session`；未知会话 → 400「会话不存在」

**联动**：

- 每会话独立单任务锁（不同 tab 并行跑任务）；onIdle（该会话浏览器全关）→ 取消该会话待决议
- 设置保存 → 重建全部会话 Agent（对话清空，前端同步清全部 tab）
- 删除用户 → 撤 token + 删其 appcore.Session + 取消并清其全部会话
- 锁序沿用：chatsMu/userMu → 释放 → cfgMu → 释放 → agentMu / taskMu，不嵌套

## 前端

- 对话 tab 动态化（终端 tab 同机制）：`{kind:"chat", uid, sessionId}`，uid 客户端稳定键、sessionId 随 hello 绑定/重绑；tabbar「+ 对话」开新 tab；最后一个对话 tab 不可关
- 消息/thinking/busy/turnStart 状态从单例改为按 sessionId 的 Map；每 tab 常驻挂载（保留回滚，同终端 pane 策略）
- `events.ts`：`subscribeChat(session, handlers)` 带 session 参数；hello.session ≠ 当前 → onReset + onHello(新id) 重绑
- confirm/ask 弹窗状态带 session，决议请求路由到对应会话
- sessionStorage 存 `{tabs:[{uid,sessionId}], activeUid}`：刷新恢复各 tab 会话并重放（浏览器 tab 级隔离天然成立）；登出全清
- client.ts 四个对话端点函数加 session 参数

## 不做（YAGNI）

历史会话列表/跨设备恢复、会话重命名、服务端会话 GC（重启清空，沿既有限制）、关闭会话的服务端通知（断 WS 即 onIdle 兜底）。

## 测试

- 后端：两会话隔离（A 提交 /ping，B 无帧）、同账号两会话各自 hub、stale/空 session WS 复活（hello.session 新 id）、POST 未知 session 400、删除用户清会话；既有 chat/WS 用例全量适配 session 参数
- 前端：events.ts 重绑逻辑单测（hello.session 变更触发 onReset+onHello）；App 多 tab 为组件层，人工验收
- 冒烟：两会话 REST/WS 隔离 + 刷新恢复

## 环境注意

`go test -race` 禁用；npm 需 `--legacy-peer-deps`。

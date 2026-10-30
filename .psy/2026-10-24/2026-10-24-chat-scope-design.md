# 对话级目标范围（取代钉住） 设计

日期：2026-10-24 · 分支：feature/web-system

## 语义

- 每个对话会话一个**目标集合**（= 该会话自有 `appcore.Session` 的钉住集）：新开对话先经 `ScopePicker` 选主机（按目录分组复选、目录整组、「全部主机」快捷项），选完才可发消息；顶部范围条「🎯 本次对话目标：a · b」点击可重开选择器。
- 省略主机的指令 = 作用于**范围内全部主机**；显式点名/标签/all 仍可突破范围（多机运维能力保留）；每轮用户消息前注入 `[本次对话目标: …]` 压制历史惯性。
- 移除：侧栏 📌 按钮与目录 📌 徽标、`GET/PUT /api/pinned`、hostView.pinned、账号级 userSess 共享表（会话各持 Session；headless `--pin-all` 走 Core 不受影响）。存量钉住数据废弃。

## API

- `PUT /api/chat/scope {session, hostIds}`：设/改该会话目标（hostIds 过滤为现存主机）→ 会话流回显「🎯 本次对话目标已设定：a · b」。
- WS hello 帧新增 `scope: [主机名]`：刷新/重连恢复显示；服务重启复活会话 scope 为空 → 前端弹回选择器。
- `POST /api/chat` 注入行改 `[本次对话目标: …]`（空目标不注入，目录提示词自会引导反问）。

## 提示词（appcore buildCatalog）

📌默认目标 → 🎯对话目标；空目标注「本次会话未设置目标主机，省略 hosts 前先确认」；有目标注「省略 hosts 作用于全部 🎯 主机；除非用户当轮明确点名，不得扩大范围」。

## 前端

- `ScopePicker.tsx`（新）：目录分组复选 + 目录整组勾选 + 全部/清空/确定；确定 → `SetChatScope` → 乐观更新范围条。
- App：`scopes: Record<sessionId, string[]>`（hello 带出）；范围空 → 渲染 Picker 且 InputBar 禁用（加 `disabled` prop，占位「请先选择对话目标」）；范围条可点击重开（编辑态按 uid）。
- Sidebar：删 📌/徽标/onTogglePin；folders.ts 删 pinnedCount；HostView 删 pinned。

## 测试

后端：scope 设置往返 + hello.scope + 未知 session 400 + 回显帧；hosts 列表无 pinned；原 pinned 用例改写。前端：folders.test 去 pinned；events.test onHello(session, scope)；client 增 SetChatScope 用例。

## 环境注意

`go test -race` 禁用；npm 需 `--legacy-peer-deps`。

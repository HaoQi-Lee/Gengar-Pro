---
psy_kind: factual
psy_version: 1
package: frontend
---

# frontend

## 概述

gengar-pro 的 Web 前端（React 18 + TypeScript + Vite）：登录页 + 服务器侧栏（状态/编辑/终端入口/目录树）、主区标签页（对话 tab 多会话多开 + 内嵌 SSH 终端）、确认/提问/主机表单/导入/设置/用户管理弹窗。对话流按事件流渲染消息块序列并叠加计时反馈（思考指示器/残留行/工具耗时/轮尾统计）；终端经 xterm.js 渲染 WS PTY 流（attach 自动补发最近 64KB 回放）。所有后端交互经 `src/api/` 传输层：`client.ts` 的 REST 封装与 `events.ts` 的 WS 订阅（对话/终端 + 连接状态回调），组件不直接碰 fetch/WebSocket。代码分割：终端（xterm）与低频弹窗 React.lazy 懒加载，首包不含 xterm。

## 文件

- `src/api/client.ts` — REST 封装：函数名对齐各端点、返回形状函数内解包；401 统一经 `setUnauthorizedHandler` 回调（App 切回登录页），登录接口走 `raw401`（透传 body 文案、不触发回调）
- `src/api/events.ts` — WS 订阅层：`subscribeChat(session, …)`（按会话连接；hello.session 不符自动重绑回调；自动重连退避 0.5s→15s + `?since=` 续传 + 断档/服务重启全量重排 + 30s 心跳；`onStatus` 连接状态回调——断线触发顶部横幅）与 `connectTerm`（发送排队待 open 冲刷 + 2min ping 保活 + 异常断开统一映射 exit；attach 时服务端先补发回放缓冲）
- `src/api/client.test.ts` / `src/api/events.test.ts` — vitest 单测（401 分流、帧去重/重排逻辑）
- `src/App.tsx` — 根组件：登录态探测（Me/401 回落，带出 isAdmin）；对话 tab 动态多开（每 tab 一个服务端会话，WS hello 绑定/重绑，sessionStorage 刷新恢复，最后一个不可关）；消息/busy/目标/确认按会话隔离（ScopePicker 空目标强制先选——pane 级遮罩、一经选定本会话不可改，hello 通告恢复）；确认/提问决议失败弹回弹窗 + toast（成功才关）；关闭带挂起弹窗的会话时提示「服务端按拒绝放行」；WS 断线顶部横幅；对话区/终端包 ErrorBoundary；终端与低频弹窗 React.lazy 代码分割；主机/弹窗/终端标签页状态
- `src/timing.ts` — 计时纯函数层：`applyChatEvent` 事件归约器、思考残留/工具耗时/轮尾统计、`settleHangingTools`（取消冻结）、`fmtDur`
- `src/timing.test.ts` / `src/form.test.ts` / `src/folders.test.ts` — vitest 单测（事件序列契约、标签解析、目录树构建）
- `src/form.ts` — 表单纯函数（`parseTags` 中英文逗号分隔去重）
- `src/folders.ts` — 侧栏目录树纯函数（`buildTree`：根主机在前→目录按码点排序→目录内直挂主机在前；`allFolderPaths` 展开初始化；递归子项计数）
- `src/types.ts` — 与 webapi hostView/chatEvent/xshellRowView/userView 的 JSON 形状一一对应的前端契约类型（ChatEvent/HostView/HostReq/ToolBlock 含计时字段/ConfirmRequestView/XshellRowView/UserView 等）
- `src/components/Login.tsx` — 登录页：用户名+密码，raw401 路径显示服务端文案（429 限速文案同样透传）；onDone 带 role（App 据此设 isAdmin）
- `src/components/Chat.tsx` — 消息流渲染：user 气泡、assistant 块序列（文本经 Markdown 渲染/思考残留/工具行含活动计时）、思考指示器（旋转帧 + 动词 + 秒数）、多主机 body 分节；`MessageView` memo 化——流式 delta 只重渲染末块
- `src/components/Markdown.tsx` — assistant 文本的 Markdown 渲染（react-markdown + remark-gfm）：输出 React 元素且默认忽略原始 HTML（无 XSS 面），链接新标签打开；代码块无语法高亮；memo——历史块的解析结果按 text 引用缓存
- `src/components/ErrorBoundary.tsx` — 渲染异常降级卡片（错误信息 + 重试渲染），对话区与终端分别包裹，异常不再整页白屏
- `src/components/TerminalPane.tsx` — 单个 SSH 终端：xterm 渲染 WS 下行流（base64 → 字节；首帧为服务端补发的回放缓冲）、输入 onData/ResizeObserver → conn.send/resize 上行、断线遮罩（exit 帧含连接异常统一映射）、右键菜单与复制粘贴（navigator.clipboard + Ctrl/Shift+Insert、Ctrl+Shift+C/V 键位）；xterm 透明底（allowTransparency）——底色与耿鬼水印由 `.term-pane` 承载
- `src/components/Sidebar.tsx` — 主机清单目录树：文件夹行（展开/收起、计数、键盘可达、拖拽放置目标）+ 主机行（状态点、>_ 开终端、✎ 编辑、两次点击删除、可拖拽归类）；右键菜单（空白=新建目录/添加主机，目录=添加主机到此目录/删除目录）——单级虚拟目录，新建即预填目录的主机表单、删除=目录内主机移回根（行内二次确认）；仅自动展开新增目录；「用户」入口仅 admin 渲染；底部账户区（当前用户名 + 退出登录）
- `src/components/UsersDialog.tsx` — 用户管理弹窗（仅 admin）：用户列表（admin 角色徽标）+ 创建表单（密码两遍一致性校验）+ 行内重置密码 + 两次点击删除（当前登录账号禁删）
- `src/components/ScopePicker.tsx` — 对话目标选择器（仅空目标时弹出，pane 级遮罩不挡 tab 栏）：按目录分组复选 + 目录整组勾选 + 全部/清空快捷项；「开始对话」经 `SetChatScope` 绑定会话，至少选一台、会话未绑定时禁用；一经选定本会话不可改
- `src/components/HostForm.tsx` — 主机录入/编辑双模式表单（秘密不回填留空保持不变；私钥走文件上传 base64 随保存提交；「测试」按钮用当前表单参数直连探测不落库；`presetFolder` 新建时预填目录；保存/测试经 client.ts）
- `src/components/ImportDialog.tsx` — XShell 上传导入：选目录（webkitdirectory 保留层级）或多选 .xsh 文件 → 读内容 base64 上传解析预览（重复禁选、重名已加后缀的最终名）→ 勾选批量提交（解析代次守卫、失败自愈重扫）
- `src/components/InputBar.tsx` — 输入区（Enter 提交/Esc 取消，仅对话 tab 存在）
- `src/components/ConfirmModal.tsx` / `AskModal.tsx` / `Settings.tsx` — 批量确认、ask_user、设置弹窗（前两者经 App 回调决议；Settings 经 client.ts 读写，掩码 API Key 未修改时原样回传）
- `src/main.tsx` — React 挂载入口
- `src/assets/gengar-logo.png` — 耿鬼 logo（Vite import 用：登录页/侧栏品牌行 + 对话空状态）
- `src/vite-env.d.ts` — Vite 资源 import 类型声明（*.png）
- `src/styles.css` — 「耿鬼紫夜」主题全部样式（紫光主色/网格底纹/药丸圆角/焦点辉光；侧栏/对话/计时/标签页/终端/登录页/品牌行）；耿鬼水印：`.pane`/`.dialog`/`.term-pane` 负 z-index `::before` 铺 logo 剪影（容器 `isolation:isolate` 防被自身背景埋掉，透明度 12–13%）

## API（关键约定，非导出库）

- REST（`client.ts`）：登录态探测 `Me`（含 role）、对话（带 session 路由）`SubmitMessage`/`CancelTask`/`ResolveConfirm`/`ResolveAsk`/`SetChatScope`（对话目标）、主机 `GetHosts`/`SaveHost`（含 keyData 上传）/`DeleteHost`/`TestHost`/`TestHostDraft`（表单内测试）、终端 `OpenTerminal`/`TerminalClose`、设置 `GetSettings`/`SaveSettings`、导入 `PreviewXshell`/`ImportXshell`、用户管理（admin）`GetUsers`/`CreateUser`/`ResetUserPassword`/`DeleteUser`
- WS（`events.ts`）：`subscribeChat` 帧 t=hello|chat|confirm|ask|confirm_done|ask_done|reset，按 seq 去重与断档检测；`connectTerm` 下行 data(base64)/exit、上行 write/resize/ping
- 后端布尔字段 omitempty（`ok:false`/`start:false` 不出现在 JSON）——判断一律真值而非 `=== true`
- cookie 会话（HttpOnly/SameSite=Lax）随 fetch/WS 自动携带；任何 401（会话过期/他处登出）触发全局回落登录页并清弹窗/忙态残留

## 依赖

**第三方：** `react`、`react-dom`、`@xterm/xterm`、`@xterm/addon-fit`、`react-markdown`、`remark-gfm`；开发链 `vite`、`typescript`、`vitest`、`@vitejs/plugin-react`

**传输：** `src/api/` 内建的 fetch/WebSocket 封装（Wails 生成绑定与 wailsjs 已随桌面形态移除）

## 设计重点

### 传输层双件套与 401 语义

`client.ts` 是唯一 fetch 出口：非登录接口遇 401 统一触发 `setUnauthorizedHandler` 注册的回调（App 回落登录页），错误文案走固定「未登录或会话过期」；登录接口传 `raw401` 跳过回调、透传服务端 `{error}` 文案（「用户名或密码错误」）。组件层只 import 具名函数，不感知 URL/方法/解包。

`events.ts` 承担全部 WS 复杂度：对话流自动重连（指数退避封顶 15s）带 `?since=lastSeq` 续传；序号断档（seq > lastSeq+1）或服务重启（hello.next 回退）触发 `onReset` 全量重排；服务端 90s 读空闲由 30s 心跳（`send("{}")`）保活。终端流不自动重连（PTY 保持，由组件决定重连时机），2min ping 防 5min 空闲掐断；浏览器 WS 未 open 时 send 静默丢弃，排队待 onopen 冲刷。

### 计时的纯函数层与打点注入

`timing.ts` 所有函数接收注入的 `now`（与轮基准 `turnStartAt`），无时钟无随机依赖（动词随机仅限 `startThinking` 内部，测试不依赖）；App 侧在 `setChat` 之外捕获 `performance.now()` 与 `turnStartRef.current` 再调用归约器，保证 updater 纯函数（StrictMode 双调安全）。跳动显示由各活动组件自持 100ms interval 本地重渲染（思考指示器一个、每个运行中工具行一个），不重渲染消息列表。

### 事件回调的过期闭包防护

WS 订阅只在登录态就绪时注册一次（effect 依赖 username），全部状态更新走函数式 setState；消息与 thinking 合并为单一 `chat` state 对象（首个输出既清 thinking 又可能插残留块，单对象保证一次原子更新）。

### 渲染性能与韧性（评审 ④）

`MessageView`/`MarkdownView` memo 化：流式 delta 只更新末条消息（消息只追加不插入，索引即稳定 key），历史消息引用不变即跳过重渲染——Markdown 不再全列表重解析。ErrorBoundary 包裹对话区与终端：渲染异常降级为错误卡片（可重试），不整页白屏。代码分割：TerminalPane（xterm ~337KB）/Settings/ImportDialog/UsersDialog 懒加载，首包 688KB→344KB。WS 断线经 `onStatus` 回调驱动顶部横幅，恢复即隐藏。

### 终端标签页的挂载策略

终端 tab 点击即建（"term-\<hostId\>" 为键），三态渲染：拨号中提示 → sessionId 就绪挂 TerminalPane → 失败显示错误 + tab 内重试/关闭（慢认证下不再零反馈）。拨号中被关的 tab，session 建立后立即 TerminalClose 回收。已连接 pane 常驻挂载（`display:none` 切换而非条件渲染）以保留 xterm 回滚缓冲；重新可见时补 fit + 聚焦。同主机去重 + 拨号中防连点（`openingRef`）；会话死亡由 WS exit 帧（含连接异常的统一映射）驱动遮罩，遮罩下停止上行。

### 测试边界

前端仅测纯函数与传输层逻辑（计时归约/表单解析/目录树/401 分流/帧去重），组件渲染与浏览器集成靠人工验收；`npm test`（vitest run）与 `npm run build`（tsc 类型检查）双门。

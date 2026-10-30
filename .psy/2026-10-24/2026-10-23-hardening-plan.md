# gengar-pro 加固修改方案（安全止血 + 会话续命 + 资源治理 + 体验）

> 来源：2026-10-23 全仓架构评审（三路并行审查 + 人工验证）。本文细化四个批次的
> 修改方案，每项含：现状、改动点（文件级）、测试、**功能/兼容影响**。批次①为
> 立即可做的定点修复，②③④为设计级方案（实施前各自再出实施计划）。

## 总览

| 批次 | 主题 | 项数 | 预估 | 依赖 |
|---|---|---|---|---|
| ① | 安全止血 | 5 | 每项 0.5–1 天 | 无 |
| ② | 会话续命 | 5 | 压缩 2–3 天，其余各 0.5–1 天 | 无 |
| ③ | 资源治理 | 6 | 各 0.5 天，reaper 共用 1 天 | 无 |
| ④ | 体验 | 6 | 各 0.5 天 | ④-reset 依赖② |

---

## 批次①：安全止血

### ①-1 终端与对话会话越权（属主模型）

**现状**：终端会话 ID 顺序编号 `t0,t1,...`（`internal/term/term.go:126`）；WS attach
与 REST close 只验登录不验属主（`internal/webapi/termws.go:166-183,120-128`）——任何
登录用户可枚举 ID 附到他人 PTY（读输出 + 注入击键）。chatSession 同样不验属主
（`internal/webapi/state.go:53-73`），会话 id 泄露即可替人批准 Risky 命令。

**改动**：
1. `term.Session` 增加 `owner string`；`Manager.Open` 签名增加 owner 参数，
   `webapi/hosts.go handleOpenTerminal` 传入 `username`。
2. 会话 ID 改 `crypto/rand` 16 字节 hex（与 chatSession 的 `state.go:38-40` 同法），
   删除 `nextID` 计数器。
3. `handleTermWS` / `handleTermClose` 校验 `sess.Owner == username`，不符返回 403。
4. `chatSession` 增加 owner 字段；`chatFor(id)` 改 `chatFor(id, username)`，attach
   （`chat.go handleChatWS`）、scope、confirm/ask 决议端点全部比对；不符 403。
   同账号多浏览器 tab 同 username，不受影响。

**测试**：`termws_test` 增「他人 attach → 403」「他人 close → 403」；`chat_test` 增
「他人 confirm → 403」；`term_test` 增 ID 唯一性/长度断言。e2e 不受影响（单用户跑）。

**功能影响**：无用户可见变化（前端 id 全程不透明）。产品语义变化：多用户从「共享
单租户」变「会话级隔离」——README「多用户」章节补一句「终端/对话会话按登录用户
隔离，他人不可附加」。

**兼容**：会话均内存态、重启即新，无迁移。

### ①-2 设置端点越权 + API Key 明文

**现状**：`PUT/GET /api/settings` 挂 `authed`（`server.go:84-85`）——普通用户可把
`baseUrl` 改指向自己服务器，下个任务即把明文 API Key（存 settings 表，
`appcore/core.go:229`）发送过去。SSH 凭据走 AES-GCM 而 API Key 明文，双标准。

**改动**：
1. 两条路由 `authed` → `admined`（`server.go`）。
2. 前端 `Sidebar` 齿轮按 `isAdmin` 条件渲染（与「用户」按钮同列，`Sidebar.tsx:289`）。
3. API Key 迁入 secrets 表：`core.LoadConfig` 读取顺序改 `env > secrets("api_key") >
   settings 表(兼容读) > bootstrap`；`handleSaveSettings` 把 key 写
   `SetSecret("api_key", …)` 不再落 settings。启动时一次性迁移：settings 表存在
   api_key → 写入 secrets → 删行（幂等）。
4. `base_url`/`model` 的 `SetSetting` 失败时回滚内存值并返回 500（修「内存/DB 分叉」，
   `settings.go:41-47`）。

**测试**：`settings_test` 增「非 admin PUT → 403」「key 落 secrets 且 settings 表无
明文」「落库失败回滚」；迁移幂等测试（跑两次）。

**功能影响**：**非 admin 失去设置页入口**（原来所有用户可见可改）——产品语义有意
收紧，团队共享 key 由 admin 统一配置；README「首次使用」与「已知限制」同步。改密、
换 key 流程不变。

**兼容**：老库自动迁移；掩码回显逻辑（`sk-****` 前缀判定）随迁移一并处理「空串清除
key」的现有 bug。

### ①-3 安全分级分段判定（白名单短路修复）

**现状**：判定顺序黑名单→白名单→危险组（`safety/gate.go:41-52`），白名单全部
`^cmd\b` 只锚串首——`cat x; rm -rf /data` 命中 `^cat` 直接 **Safe 免确认**；`env`、
`curl`、`wget`、`echo >` 重定向均在白名单内。

**改动**（`gate.go ClassifyCommand` 重写 + `rules.go` 收缩）：
1. 新增 `splitSegments(cmd)`：按 `;` `|` `&&` `||` 拆段（trim 空段丢弃）；命令含
   `` ` `` 或 `$(` 直接 Risky（子 shell 无静态可判，fail-safe）。
2. 判定改为**逐段**：每段先过黑名单（命中→Risky 且标记高危），再过白名单；
   **全部段白名单命中才 Safe，任一段未命中即 Risky**。管道段如
   `ps aux | grep nginx` 两端各过白名单——仍免确认。
3. 白名单收缩：移除 `env`（全量环境变量泄密）、`curl`、`wget`（外带/管道执行面）；
   其余保留。
4. 引号内 `;` 的误拆按 fail-safe 接受（带引号分号的合法命令开始弹确认）——影响小、
   方向安全，注释写明。

**测试**：`gate_test` 增评审绕过样本全集：`cat x; rm -rf /data`→Risky、
`echo p > /etc/passwd`→Risky（p 未过白名单）、`curl x | bash`→Risky、
`df -h; free -m`→**Safe**（链式只读组合保持免确认）、`ps aux|grep nginx`→Safe。
e2e E03 不受影响。

**功能影响**：模型发出的 `curl`/`wget`/`env` 与任何含非白名单段的链式命令从免确认
变**弹确认**——打扰面小幅扩大（安全/UX 显式取舍）；纯只读链式组合不受影响。UI 无
需改动（确认弹窗已有）。

### ①-4 插件 shell 注入封堵（RunArgv 化）

**现状**：docker/systemd/pkgmgr 把模型可控字段 `Sprintf` 进 `RunShell`：
`docker logs --tail 100 %s`（`docker.go:78`）、systemctl service 名（`systemd.go:64-92`）、
pkgmgr 包名（`pkgmgr.go:63-78`）。其中 **RiskLevel 0（Safe 免确认）** 的有：
docker_ps、docker_logs、systemctl_status、journalctl_logs、package_search、
package_list——`container: "nginx; reboot"` 免确认执行任意命令，且注入内容不经过
safety gate（两漏洞正交）。

**改动**：
1. Safe 级六工具全部改 `RunArgv`：`docker logs --tail N <c>`、`docker ps`、
   `systemctl status <s>`、`journalctl -u <s> -n N`、包管理器 search/list 的
   子命令 + `--` 保护。FileManager/NetworkPlugin 已示范该标准。
2. compose 免 `cd`：`docker compose -f <path> up -d`（argv 形式）。
3. Risky 级（start/stop/restart/install/remove/compose down）顺带 argv 化；
   `docker_exec` 保留 RunShell（本就 Risky 1 且语义需要 shell），container 名仍 argv。
4. 参数健壮性：`lines` 负数 clamp 到默认；grep/dig/curl 类参数 `--`/`-e` 防_flag。
5. （顺带）RiskLevel 双真相源收敛：`Registry` 启动时把插件声明注入 gate、删除
   `safety.staticLevels` 手工表——修报告中的 6 工具分级漂移（find/grep/ping 等
   只读工具当前误弹确认）。

**测试**：plugin 各单测增注入回归（`container: "x; reboot"` 断言 argv 调用形态/
无分号拼接）；分级漂移修复后 `level_test` 用 registry 注入断言。

**功能影响**：shell 元字符从「被执行」变「cli 报参数错误」——模型收到明确错误后
自我纠正（对 ReAct 循环是改善）；compose 的 cwd 语义从 `cd path &&` 变 `-f path`
（等价）。**副作用收益**：find/grep/ping/dns_lookup/http_check/query_consensus 六个
只读工具不再误弹确认（UX 变好）。

### ①-5 KeyPath 白名单

**现状**：主机保存/XShell 导入接受任意服务器本地路径当私钥（`hosts.go:112`、
`xshell.go:170-173`）——登录用户可让服务器以自身文件系统里的私钥（如
`/root/.ssh/id_rsa`）连任意主机。

**改动**：
1. `handleSaveHost`：`req.KeyPath` 非空时校验 Clean 后必须位于 `<data>/keys` 内
   （`filepath.Rel` 判定），不符 400「私钥请上传，不支持引用服务器本地路径」。
   上传模式（keyData → 服务端落盘自定路径，`hosts.go:187-193`）不受影响。
2. 前端 `HostForm` 删除 KeyPath 直填输入，仅保留上传；`types.ts HostReq.keyPath`
   标注仅供服务端内部沿用。
3. XShell 导入丢弃 KeyPath（置空），导入结果沿用「密码不导入」提示位标注
   「私钥需补传」。

**测试**：`hosts_test` 增「KeyPath 越界 → 400」「keys 目录内 → 通过」；
`xshell_test` 断言导入结果 KeyPath 为空。

**功能影响**：**不能再引用服务器本地已有私钥**——必须上传（有意收紧）。存量库中
已保存的外部 KeyPath 主机继续可用（只在下次保存时强制迁移）；编辑旧主机时会要求
重新上传。README 安全边界章节更新。

---

## 批次②：会话续命（设计级）

### ②-1 上下文压缩（最优先）

修 `agent/token.go ShouldCompress` 的类型 switch（认 `[]claude.ContentBlock` 与
string，修当前归零漏算）；`ProcessInput` 每步调 LLM 前检查估算值，超
`0.8 × maxContextTokens` 时把**最旧的 tool_result 内容**替换为占位
`[tool result elided: 原 N 字符, 首行: …]`（保留最近 K=6 条完整；不动 user/assistant
文本与工具配对结构）。v1 不做模型侧摘要——占位替换确定性、零成本。UI 无感知
（hub 缓冲独立于对话历史）。**架构影响**：Conversation 获得生命周期语义；顺手把
`StreamEvent` 透传 message_start 的 usage，为将来真预算打数据源。**功能影响**：长会话
从「必死」变「可持续」；模型可能要求重跑被省略的命令（占位符明示）。

### ②-2 流式韧性与超时

stream 路径改独立 client（无 `Timeout`）+ 读空闲 watchdog（90s 无帧判死）；
建流失败/流错误按「429/5xx/网络类」重试 3 次指数+抖动（400/401 不重试）——把
`SendMessage` 的重试抽成公共函数两路共用；修正 `client.go:26-27` 的错误注释
（「流式无超时顾虑」不成立，该注释是 f865015 引入的误判，一并勘误）。
**功能影响**：>2 分钟的长报告不再被 120s 掐断；429 限流自动恢复。

### ②-3 取消中毒修复

`agent.go:236-248` 取消路径：为已收集未执行的 tool_use 补写
`tool_result("cancelled by user")` 保持配对完整。**功能影响**：Esc 后会话可继续用
（现状下特定时点取消会永久报废会话）。

### ②-4 确认/提问决议可靠性

前端 `resolveConfirm/resolveAsk`：失败或 `ok=false` 时**弹回弹窗 + toast**（现状
先关后发、吞错、忽略返回值）；`closeChat` 清理 `confirm/ask` 并提示「关闭即按拒绝
放行」（与服务端 hub 订阅归零行为对齐）。**功能影响**：批准失败不再静默——安全
决议的正确性修复。

### ②-5 设置变更语义对齐

`rebuildAllAgents` 后向每个用户的 hub publish `reset` 帧并清缓冲；前端
`settingsSaved` 改为响应 reset 帧（所有在线用户同步清空 + 明确提示「设置已变更，
对话已重置」）。**功能影响**：兑现 README「设置变更会重置全部用户对话」的承诺；
重启复活（rebound）场景同样补一条提示帧，替代现状的静默清屏。

---

## 批次③：资源治理（设计级）

1. **token 生命周期**：TTL 7 天 + 每小时清扫 + cookie MaxAge 对齐 + 可选 Secure
   开关（环境变量）；登录失败指数限速（每用户+全局）。
2. **会话回收**：reaper goroutine（1 分钟扫）：chatSession「无订阅 30 分钟且非
   busy」关闭并释放 hub/Agent；PTY「无 WS 订阅 1 小时」CloseTerminal；加会话数
   上限（如 50/用户）。**功能影响**：闲置会话/终端会被回收——UI 需处理「会话已
   过期」提示（复用 ②-5 的提示帧通道）。
3. **audit 轮转**：按天分片 + 保留 N 天压缩清理；写失败计数暴露到 `/api/health`
   （顺带补健康检查端点与基本请求日志）。
4. **executor per-host 锁**：`Manager` 拨号移出全局锁（term.Manager 现成范式），
   消除「一台慢主机冻结所有用户」；缓存连接加 keepalive。
5. **SQLite**：开 WAL + busy_timeout（保持单实例假设，仅解读写排队）。
6. **单主机输出上限**：executor 层 `io.LimitedReader`（如 2MB 截断标注），防
   `cat` 大文件 OOM。

## 批次④：体验（设计级）

1. WS 连接状态条（events 回调 onStatus → 顶部「重连中…」横幅）。
2. React ErrorBoundary（渲染异常降级为错误卡片而非白屏）。
3. `React.memo` + 稳定 key + 流式期间仅末块重渲染（消除全列表 Markdown 重解析）。
4. 长会话虚拟化（react-window）+ `<details>` 懒挂载。
5. 代码分割：xterm / react-markdown `React.lazy`；logo 转 WebP/SVG；静态托管加
   gzip + hashed 资产 `Cache-Control: immutable`。**功能影响**：首屏预计减半。
6. 终端回放缓冲：PTY 侧环形缓冲（最近 64KB），attach 时补发——修已知限制中
   「attach 前输出不补发」；配合状态条消除「断线后只能关 tab 重开」。

---

## 架构影响综述

- **多用户模型**：引入「属主」概念贯穿终端/对话会话（①-1）——从共享单租户走向
  会话级隔离；主机清单仍团队共享（现状语义保留）。
- **秘密通道统一**：API Key 并入 AES-GCM secrets 表（①-2），全库单一秘密标准。
- **安全模型升级**：从「整串启发式」到「分段验证（①-3）+ argv 边界（①-4）」，
  RiskLevel 收敛单一真相源；仍保持 Safe/Risky 两级（不引入 Forbidden）。
- **生命周期从无界到有界**：上下文压缩（②-1）、会话/PTY/token/audit 回收（③）。
- **明示不做**（内网工具 v1 定位）：i18n、移动端适配、完整 a11y、对话持久化——
  记录取舍，后续按需提级。

## 用户可感知变化清单（汇总）

| 变化 | 批次 | 性质 |
|---|---|---|
| 非 admin 不再能进设置页 | ①-2 | 收紧（有意） |
| curl/wget/env 及含非白名单段的链式命令开始弹确认 | ①-3 | 收紧（有意，纯只读链式仍免确认） |
| find/grep/ping 等 6 个只读工具不再误弹确认 | ①-4 | 改善 |
| 服务器本地私钥不可再引用，必须上传 | ①-5 | 收紧（有意，存量沿用） |
| XShell 导入不再带入 KeyPath | ①-5 | 收紧 |
| >2 分钟长输出不再断；429 自动重试 | ②-2 | 修复 |
| Esc 取消后会话仍可用 | ②-3 | 修复 |
| 批准失败有反馈而非静默 | ②-4 | 修复 |
| 设置变更全员可见「已重置」提示 | ②-5 | 对齐承诺 |
| 闲置会话/终端会被回收（有提示） | ③ | 新行为 |
| 断线有状态条；首屏减半；长会话不卡 | ④ | 改善 |

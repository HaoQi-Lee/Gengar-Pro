# 审计事件补全设计（用户输入与关键操作）

- 日期：2026-11-01
- 状态：已与用户确认（只补审计事件，不引入运行日志层）
- 范围：webapi 层关键操作 + 用户输入 + 决议类事件的审计补全；分支 `feature/audit-events`

## 1. 背景与目标

现状（2026-11-01 摸底）：agent 工具调用审计已全覆盖（拒绝/分发失败/完成三处），
webapi 有三组合成事件（用户管理/终端/设置），但**登录（含失败）、主机 CRUD
（含私钥上传）、XShell 导入、chat 输入原文、确认弹窗决议、ask 决议、任务取消
全部空白**；且存在三处既有缺陷。本设计补全这些事件并修复缺陷。

成功标准：安全事件可回答「谁在何时从哪登录/失败了几次」「谁改了哪台主机」
「谁批准了哪个对话里的哪批命令」「用户对 Agent 说了什么」——全部可从
audit JSONL 单一来源检索；E2E 断言语言可直接消费（变更集 2 依赖）。

## 2. 决策记录

| 决策点 | 结论 | 备注 |
|---|---|---|
| 单一 JSONL | 不分表不加字段，Tool 名即事件名 | 旧条目与现有断言零破坏；webapi 事件继续 Tool 约定 |
| 运行日志 | 本期不做（访问日志/slog/panic 中间件另议） | 用户决策；审计≠运维日志 |
| chat 原文 | 全文入审计，8KB 截断 | 内网审计场景需要；与工具 Output 截断同哲学 |
| 凭据 | **私钥内容、api_key 值绝不入审计** | host_save 只记「私钥已上传/未变更」；settings_save 只记变更字段名清单 |
| 拒绝来源可区分 | disconnect 自动取消 ≠ 用户主动拒绝 | agent 侧线程化拒绝原因 |
| 终端击键 | 不记 | PTY 内容不可追溯，YAGNI |
| WS 重放/reset | 不记 | 系统行为非用户操作 |

## 3. 事件规格

统一注入 helper（webapi 侧新增，替换现有三处散写 users.go:220 / termws.go:181 /
settings.go:77 的重复代码）：

```go
// auditOp 记一条 webapi 合成事件（Tool=事件名约定不变）。
func (s *Server) auditOp(user, tool, output string) // input=nil, hosts=nil
```

### 3.1 认证（auth.go）

| 事件 | 字段 | 触发 |
|---|---|---|
| `login_ok` | output=`user=<name> ip=<remote>` | 登录成功发 cookie 时 |
| `login_fail` | output=`user=<attempted> ip=<remote>` | 密码错误/用户不存在（每次都记）；限速 429 拒绝不产生事件（未产生认证判定，且防灌日志） |
| `logout` | output=`user=<name>` | 登出清 cookie |

限速锁定本身不单独发事件（连续 login_fail 条目已可推导），保持事件粒度=用户动作。

### 3.2 主机管理（hosts.go）

| 事件 | 要点 |
|---|---|
| `host_save` | input 记 `{name, addr, port, auth_method, key_uploaded:bool}`（新增或更新同事件）；私钥/密码内容不出现 |
| `host_delete` | input 记 `{name}` |
| `host_test` | output 记连通结果（ok/失败原因摘要） |

### 3.3 导入（xshell.go）

| 事件 | 要点 |
|---|---|
| `xshell_import_preview` | output=`parsed=<n> hosts` |
| `xshell_import_commit` | output=`imported=<n> hosts` |

### 3.4 对话与决议（chat.go / agent.go / headless）

| 事件 | 要点 |
|---|---|
| `chat_input` | input=`{"text": <原文≤8KB>}`；web 侧带 user+session，headless 侧 user 空 |
| `confirm_resolve` | input=`{"session":<sid>,"confirm_id":<pid>}`，output=`decision=approve\|deny`（仅用户 REST 决议成功时记；一个 pid 对应一次弹窗，内含当批全部 Risky 工具） |
| `ws_auto_cancel` | output=`session=<id> cancelled=<n>`（浏览器全断开 onIdle 路径取消待决议时记；n=0 不记） |
| `ask_resolve` | input=`{"session":<sid>,"ask_id":<pid>}`，output=`answer=<原文截断 256>` |
| `task_cancel` | output=`session=<id>`（用户主动取消） |
| `ask_user`（工具名） | agent.go ask 早退路径补一条工具调用审计（input=问题，output=用户回答），闭掉绕过审计的缺口 |

### 3.5 既有缺陷修复

1. `user_create` 缺 operator：handleCreateUser 签名找回 username 参数（users.go:56 `_ string`）
2. `settings_save` 补变更字段名清单（仅字段名，不含值）
3. 拒绝来源可区分（分层方案，**不改 ui.Sink 接口**）：agent.go:455 的
   `user rejected` 保持通用；区分由 webapi 层承载——用户 REST 决议走
   `confirm_resolve`，断连自动取消走 `ws_auto_cancel`（CancelPendings 改为
   返回取消数，onIdle 包装处 n>0 才记；handleCancelChat 的用户取消已有
   task_cancel 承载，不计入 ws_auto_cancel）

## 4. 实现要点

- agent.AuditLogger 接口不变（webapi 合成事件已是现有模式，不扩接口）
- 审计写失败不阻塞业务路径（现有 audit.Logger 语义：返回 error 由调用方忽略或计数）
- 每个新事件的字段在单测中钉住（webapi 各 handler 测试 + agent 测试）
- headless 的 chat_input：main.go stdin 读入处注入（user 空串，与现有约定一致）

## 5. 测试

- webapi 各 handler 单测：动作后断言审计条目（fake audit logger 或读临时目录分片）
- agent 单测：ask_user 审计、拒绝 vs 断连取消的 output 区分
- E2E 断言（变更集 2 的 `audit_field` 语言承接，本变更集先在单测层钉死）

## 6. 明确不做

运行日志层（access log/slog/panic 中间件）、终端击键、WS 重放/reset、
审计检索 API、分表存储。

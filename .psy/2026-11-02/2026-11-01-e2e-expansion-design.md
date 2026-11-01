# E2E 扩张设计（headless 档扩展 + webapi 档新建）

- 日期：2026-11-01
- 状态：已与用户确认（两层都做）
- 范围：分支 `feature/e2e-expansion`，**基于 `feature/audit-events` 分支**（webapi 档要断言新审计事件）

## 1. 背景与目标

现状：E2E 仅 headless 档（桩 LLM 回放 + Docker sshd + YAML 用例，8 用例只动
4 个工具），断言语言只有 Tool/Hosts/Approved 三字段；webapi/WS 层零 E2E
（全靠 httptest 单测）。目标：补齐「全面」——headless 档扩用例与断言语言，
新建 webapi 档驱动真实 `gengar-server` 二进制走 HTTP/WS 全链路。

成功标准：登录/限速、主机 CRUD、chat 全链路（含确认决议）、断线重连续传、
多用户隔离、redis 三工具都有端到端用例且在 Docker 可用时全绿。

## 2. 决策记录

| 决策点 | 结论 | 备注 |
|---|---|---|
| webapi 档形态 | 子进程驱动 `gengar-server` + 真实 HTTP/WS 客户端 | 与 headless 档同哲学：测整装二进制而非进程内构造；复用桩 LLM（httptest 监听真实端口，跨进程可用） |
| 分层 | 单测 → integration（执行层）→ e2e headless（agent 链路）→ e2e webapi（web 层） | 各档边界不变 |
| redis 用例环境 | sshd 容器内 `apk add redis` + 启动时造数据 + SAVE 生成 dump | redis_rdb 走 SFTP 取回真 dump 全链路解析 |
| 断言语言 | 加 `audit_field`（tool+字段匹配：user/session/is_error/output/input 正则） | 现有 8 种断言不动 |
| 上下文压缩用例 | 不做 | 桩按请求序号回放，构造压缩触发条件会 flaky；留待真实 LLM 档 |
| build tag | `//go:build e2e`（两档同 tag，`-run` 区分） | 与现有跑法一致 |

## 3. headless 档扩展

### 3.1 断言语言（cases.go + harness.go）

新增断言类型：

```yaml
- audit_field:            # 按 tool 匹配条目并断言字段
    tool: login_fail
    user: ""              # 可选：精确匹配（支持 "" 表示空）
    session_re: ""        # 可选：正则
    is_error: true        # 可选：布尔
    output_re: "ip="      # 可选：正则
    input_re: ""          # 可选：正则（匹配 input 的 JSON 文本）
    count: 1              # 可选：匹配条数精确值（默认 >=1）
```

实现为纯函数，与现有断言同签名风格。

### 3.2 新用例

| 用例 | 内容 | 关键断言 |
|---|---|---|
| E08-redis-tools | 容器装 redis + 造 6 键 dump；脚本走 redis_discover → redis_cli(DBSIZE) → redis_rdb | tool_called×3；audit_field 断言 redis_rdb 的 output 含「总键数 6」 |
| E09-multi-turn-chain | 一轮对话内 memory_write → 下一轮 memory_read → 汇总回答 | 两工具按序、跨轮状态正确 |
| E10-error-path | hosts 指向不存在主机名 → execute_command | stdout 含 FAILED 分节；audit_field is_error=true |

E08 环境准备：harness 加 `startRedisHost(t)`——startHosts 后 docker exec 装
redis、起实例、SET/HMSET/RPUSH/ZADD/SADD、SAVE；容器内 dump 路径供 redis_rdb
显式 path 参数使用。

## 4. webapi 档新建（internal/e2e/webapi/）

### 4.1 框架组件

- **serverproc.go**：现场编译 `gengar-server`（复用 headless 的构建缓存模式）；
  子进程启动参数/环境：数据目录、桩 LLM base_url+api_key、监听端口（空闲端口）；
  等待 `/api/health` 就绪；退出清理
- **client.go**：带 cookie jar 的 http.Client（登录/REST）；gorilla/websocket
  客户端（chat WS、term WS）；断线重连 + `since` 续传辅助
- 复用：stubllm 回放引擎（原样）、sshd 容器（startHosts）、readAudit

### 4.2 用例（YAML 化程度降低——webapi 用例以 Go 表驱动为主，YAML 只做脚本注入）

| 用例 | 流程 | 断言 |
|---|---|---|
| W01-login | 登录成功→me→登出；错误密码×N→限速 429 | HTTP 状态码序列；audit_field: login_ok/login_fail 条数与 output 含 ip |
| W02-host-crud | 建 sshd 主机（含私钥字段上传形态）→ 列表 → 连通测试 → 删 | REST 响应；audit_field: host_save/host_test/host_delete；**input 不含私钥内容**（正则断言） |
| W03-chat-confirm-flow | 登录→开 chat WS→发指令（脚本含 Risky 工具）→收确认请求→REST 批准→工具在 sshd 容器真执行→收完成事件 | WS 事件序列；audit 链三连：chat_input→confirm_resolve(approve)→tool_call(approved=true) |
| W04-disconnect-cancel | 同 W03 前半，但批准前**断开 WS**→工具 auto-cancel→重连→since 续传拿到历史 | audit: confirm_resolve(decision=disconnect_auto_cancel) + tool_call(output=auto-cancel)；重连后事件回放完整 |
| W05-isolation | 两个普通用户各建私有主机 + 一个 admin | 用户 A 列表/解析看不到 B 的主机（REST 404/空列表 + catalog 不含）；admin 全见；audit 各自 user 戳正确 |

W06（XShell 导入+settings 审计）、W07（term WS attach/回放）为可选扩展位，
本期默认不做，框架能力到位即可。

### 4.3 稳定性约束

- 每用例独立数据目录 + 独立 server 进程（隔离 cookie/审计分片），Docker 缺失
  即 Skip（同现有约定）
- WS 读取带超时；server 子进程 stdout 收进 buffer 供失败诊断（不污染测试输出）
- 桩 LLM 按请求序号回放的模式不变（webapi 档脚本即对话回合脚本）

## 5. 测试与跑法

```bash
go test -tags e2e ./internal/e2e/ -v                     # headless 全量
go test -tags e2e ./internal/e2e/webapi/ -v              # webapi 档
go test -tags e2e ./internal/e2e/webapi/ -run W03 -v     # 单用例
```

框架自身（断言纯函数、client 辅助）带无 Docker 依赖的单测。

## 6. 明确不做

上下文压缩用例（flaky 风险）、真实浏览器（前端 JS 不在射程）、W06/W07 默认、
性能/压测、跨版本迁移。

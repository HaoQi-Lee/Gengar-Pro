---
psy_kind: factual
psy_version: 1
package: internal/appcore
---

# internal/appcore

## 概述

`internal/appcore` 是 gengar-pro 两条入口（Web 服务 / headless）共享的组装层：打开数据目录（SQLite 清单 + 记忆目录），持有连接管理与钉住集合，实现 `plugin.TargetResolver`（目标引用 → 主机名 / 绑定执行器的目标），生成注入系统提示词的主机清单目录，并提供 `BuildAgent` 一站式组装完整 Agent。`Session` 是 Core 之上的单用户视图（Web 多用户场景：钉住/默认目标按用户隔离，且清单按属主视角过滤——`NewSessionFor`；headless 直接用 Core 方法与全局视图 `NewSession`）。

## 文件

- `core.go` — `Core` 全部实现：数据目录管理、SetSecrets、钉住集合、Names/Resolve 解析、CatalogText、LoadConfig（含 settings.json 引导）、BuildAgent
- `session.go` — `Session`：Core 之上的单用户视图（钉住集合隔离 + 按用户默认目标组装 Agent）
- `core_test.go` — Names/Resolve 对称性、配置加载优先级、settings.json 引导（字段级/落库/缺文件）单元测试
- `session_test.go` — 钉住隔离、Session/Core 行为一致性等单元测试

## API

- `type Core struct` — 组装核心：host.Store + SecretStore + executor.Manager + 记忆目录 + 钉住集合（互斥保护）
- `OpenAt(dataDir string, secrets host.SecretStore, timeout time.Duration) (*Core, error)` — 指定数据目录；目录结构：`gengar.db` · `memory/` · `audit.jsonl`（入口各自开）· `known_hosts`（executor 自管，入口设路径）；不建父目录，由入口负责
- `(c *Core) Store() *host.Store` — 透出清单存储
- `(c *Core) DataDir() string` — 数据目录路径
- `(c *Core) Secrets() host.SecretStore` — 透出秘密存储（webapi 主机表单写密码用）
- `(c *Core) SetSecrets(secrets host.SecretStore)` — 替换秘密存储并重建连接管理器；供 server 入口在 OpenAt 之后装配 EncryptedStore（OpenAt 先传占位实现），必须在任何 SSH 连接建立前调用
- `(c *Core) ExecutorFor(h *host.Host) (executor.Executor, error)` — 透出连接管理（连通性测试用）
- `(c *Core) Close() error` — 关闭全部 SSH 连接与清单存储
- `(c *Core) SetPinned(ids []string)` / `(c *Core) PinnedIDs() []string` — 钉住集合（UI 的默认目标）读写；Core 级为 headless 单用户语义，Web 多用户用 Session
- `(c *Core) Names(refs []string) (names, unresolved []string)` — `plugin.TargetResolver` 实现：展开引用为主机名（refs 为空 → 钉住集合），不建立连接
- `(c *Core) Resolve(refs []string) (targets []plugin.Target, unresolved []string, err error)` — 展开引用并绑定执行器（复用/建立 SSH 连接）
- `(c *Core) CatalogText() string` — 生成注入系统提示词的主机清单目录（含 📌 默认目标标记与目标引用规则，确定性输出）
- `LoadConfig(store *host.Store, secrets host.SecretStore) *config.Config` — 配置优先级链：api_key 走密文通道（env > secrets（AES-GCM，设置页保存）> 旧 settings 明文（读出即迁入 secrets 并清明文，幂等）> 引导）；base_url/model 为 SQLite settings > 环境变量 > 引导 > 默认
- `(c *Core) BuildAgent(cfg *config.Config, sink ui.Sink, csns *consensus.Consensus, auditLog agent.AuditLogger, sessionID string, extra ...agent.Option) (*agent.Agent, error)` — 组装完整 Agent：claude 客户端、安全门、全部 11 个插件、记忆、resolver、catalog/audit/session 选项；auditLog 接口化（webapi 传按用户盖 User 戳的包装器，禁用审计须传字面 nil——见 agent.AuditLogger 契约），归调用方所有，Core.Close 不关它
- `(c *Core) NewSession() *Session` — 创建全局视图会话（清单不按属主过滤；headless 单用户语义）
- `(c *Core) NewSessionFor(owner string, isAdmin bool) *Session` — 创建用户视角会话：普通用户只解析自己名下主机（Agent 目标解析/目录隔离），admin 解析全表；视角在创建时快照（角色不可变前提）
- `type Session struct` — 每登录用户一个：钉住集合与 Agent 会话按用户隔离，清单按 Session 视角（global / owner+isAdmin）过滤；连接/设置全局共享
- `(s *Session) SetPinned(ids []string)` / `(s *Session) PinnedIDs() []string` — 本会话钉住集合读写（防御性拷贝）
- `(s *Session) Names(refs []string)` / `(s *Session) Resolve(refs []string)` — 与 Core 对应方法同语义，钉住集合读本会话的
- `(s *Session) CatalogText() string` — 按本用户钉住集合生成清单目录（📌 标记随用户不同）
- `(s *Session) BuildAgent(cfg, sink, csns, auditLog agent.AuditLogger, sessionID, extra...)` — 组装绑定本会话的 Agent（默认目标/目录按本用户钉住集合）

## 依赖

**内部包：** `agent`、`audit`、`claude`、`config`、`consensus`、`executor`、`host`、`memory`、`plugin`、`safety`、`ui`（几乎全量——组装层的本性）

**第三方库：** 无直接依赖（经内部包间接引入）

**标准库：** `fmt`、`os`、`path/filepath`、`strings`、`sync`、`time`

## 设计重点

### Names ≡ Resolve 对称不变量

确认弹窗显示的主机必须恒等于实际执行的主机。`Names` 与 `Resolve` 共用同一个 `matchedHosts` 匹配阶段（空 refs → 钉住集合；否则 `host.Resolve`），结构性保证两者对相同 refs 产出相同主机集合；集成测试在真连接下实测该对称性。

### 单次快照

`CatalogText` 用一次 `inventory()` 读表同时产出 hosts 与 pinned 交集，避免 `pinnedHosts` 二次读表引入两份快照不一致的窗口（钉住标记错标已删除主机等）。

### 一份组装，两条入口；Core/Session 共用自由函数

Web 服务与 headless 共用 `OpenAt` + `LoadConfig` + `BuildAgent`，保证两条路径的插件集合、提示词、审计行为一致；consensus 加载失败 headless 直接退出（nil Consensus 虽有防护但缺共识目录、query_consensus 恒报错），webapi 降级 nil 继续服务（长驻多用户进程，因一段嵌入 YAML 损坏而拒绝全部请求代价过高）。Core 与 Session 的匹配/目录/组装逻辑分别下沉为共用自由函数 `matchHosts`/`buildCatalog`/`buildAgent`（resolver 与 catalog 由调用方注入：Core 传自身、Session 传自身），单一实现杜绝两条路径行为漂移。

### Session 按用户隔离

webapi 每个登录用户各持一个 `Session`：钉住集合（= 省略 hosts 时的默认目标）与 Agent 互不干扰；清单经 `NewSessionFor(owner, isAdmin)` 按属主视角过滤（普通用户的 CatalogText/Names/Resolve 只含自己主机——Agent 看不到他人的主机名；admin 视角 = 全表）；SSH 连接缓存、设置全局共享。`NewSession()` 保留全局视图（headless 单用户路径）。Names ≡ Resolve 对称不变量在 Session 上同样成立（共用 matchHosts）。pinned 集合持有划转后不再属于该用户的主机 ID 时，解析被视角自然过滤（与跳过已删除主机同语义）。

### settings.json 一次性引导

`bootstrapFromClaude`（未导出，`LoadConfig` 内部调用）对仍为空的字段从 `~/.claude/settings.json` 补齐并落库（键名与设置页 `SaveSettings` 一致：`api_key`/`base_url`/`model`）。语义要点：字段级独立补齐（已有值永不覆盖）；内存先生效、`SetSetting` 失败不报错（下次启动重试导入）；二次启动时 SQLite 已有值、引导层自然短路——`~/.claude/settings.json` 后续变化不会覆盖已初始化配置。路径经参数注入以便测试，不覆写 `HOME`/`USERPROFILE`。

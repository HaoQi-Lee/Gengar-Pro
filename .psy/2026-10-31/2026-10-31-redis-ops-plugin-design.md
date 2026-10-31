# Redis 运维插件设计（discover / cli / rdb 三件套）

- 日期：2026-10-31
- 状态：已与用户逐项确认
- 范围：gengar-pro 新增 Redis 中间件运维插件（发现、透传运维、RDB 文件分析）

## 1. 背景与目标

gengar-pro 已有成熟的工具插件体系（`internal/plugin`：Plugin 接口 + Registry，10 插件
35 工具），但中间件（MySQL/Redis 等）运维能力为零。目标是以插件式扩展补上这一类能力，
第一期为 Redis 三个工具，验证「中间件运维插件」的落位模式，为后续 MySQL 等铺路。

成功标准：

1. Agent 能在对话中完成「发现主机上的 Redis 实例 → 带凭据执行任意 Redis 命令 →
   离线分析 RDB 文件产出键空间报告」三类任务，全程过安全门与审计。
2. 零 go.mod 新依赖、零前端改动、零核心框架改动（除 executor 加一个文件取回方法）。
3. 单元测试全走 fakeExec，不需要真实 Redis/SSH。

## 2. 决策记录

| 决策点 | 结论 | 备注 |
|---|---|---|
| 执行通道 | SSH + CLI 统一通道 | 与项目「一切经主机 SSH」哲学一致；协议直连留接口可能性，本期不做 |
| 实例建模 | 不建实例实体 | 最初拟新增 memory `instances` 分类，范围收窄后取消；凭据作为工具参数逐次传入，Agent 可用现有 memory 工具自行记忆，不特殊支持 |
| 凭据安全 | 明文进对话/审计，本期不做加固 | 用户明确决策；迁移 SecretStore 的路径见 §9 |
| 插件形态 | 平铺插件（同 docker/systemd） | 不建中间件子框架、不做声明式清单；第三个中间件进来时再评估沉淀共享层 |
| 部署 | 本期不做 | 曾设计「Skill 引导部署 + config_gen 工具」方案（方案 A），用户收窄范围时移除，模式记录于 §9 |
| 探伤/优化 | 本期不做独立工具 | 探伤报告、bigkeys 等后续从 redis_cli 透传用法中沉淀 |
| RDB 分析深度 | 完整键空间分析，内置 rdr 解析核心 | rdr（xueqiu/rdr，Apache-2.0，纯 Go）；不取其 web UI/CLI，只取解析核心 |
| MySQL | 本期不做 | 前一版设计（discover/probe/query/backup/slowlog 五工具）已在对话中细化，后续按同模式重启 |

## 3. 架构落点

```
internal/plugin/redis.go          # RedisPlugin：3 工具（无状态结构体，argv 形式执行）
internal/plugin/redis_test.go     # fakeExec 表驱动测试
internal/rdbparser/               # RDB 解析与聚合（解析核心内置自 rdr，聚合层自研）
internal/rdbparser/LICENSE        # rdr 的 Apache-2.0 许可证归属
internal/executor/executor.go     # Executor 接口 +FetchFile
internal/executor/local.go        # Local 实现（文件复制）
internal/executor/ssh.go          # SSH 实现（SFTP 流式取回，复用 pkg/sftp）
internal/appcore/core.go          # buildAgent() 加 reg.Register(plugin.NewRedisPlugin())
```

- 与现有插件完全同构：无状态、`Targeting=true` 自动获得多主机 fan-out（8 并发）、
  确认弹窗、审计、聚合输出预算（10000 字符/按主机截断）。
- 所有远程命令走 `exec.RunArgv`（不经 shell 重解释），与 network/diagnostic 插件
  的注入防护一致。
- 风险分级即安全门行为，RiskLevel 单源在 ToolDefinition。

## 4. 工具规格

### 4.1 redis_discover（Safe，Targeting）

发现主机上的 Redis 实例。

InputSchema：`{"type":"object","properties":{"hosts":{...}}}`（仅 fan-out 目标参数）。

行为（全部只读）：

1. `pgrep -af redis-server` → 进程与完整命令行（cmdline 里可能带 `--port`）。
2. `ss -tlnp` → 解析出属于 redis-server 进程的监听端口（覆盖 6379、哨兵 26379、
   集群总线 16379 及自定义端口）。
3. 逐候选端口：`redis-cli -h 127.0.0.1 -p <port> PING`
   - `PONG` → 无密码，继续 `redis-cli ... INFO server` 取 `redis_version`、`redis_mode`
     （standalone/sentinel/cluster）；
   - `NOAUTH` → 标注「需密码」，不尝试爆破；
   - 连接失败/超时 → 标注「端口在听但非 Redis 响应」。

输出：实例清单文本（端口 / 版本 / 模式 / 是否需密码 / 进程 PID），逐实例一行，
便于 Agent 直接转述或自行 memory_write 记忆。

### 4.2 redis_cli（Risky，Targeting）

任意 Redis 命令 argv 透传。

InputSchema：

```json
{"type":"object","properties":{
  "args":{"type":"array","items":{"type":"string"},"description":"Redis command as argv words, e.g. [\"GET\",\"foo\"], [\"CONFIG\",\"GET\",\"maxmemory\"]"},
  "port":{"type":"integer","description":"Port (default 6379)"},
  "password":{"type":"string","description":"Auth password if REQUIRED (NOAUTH)"},
  "hosts":{...}},
 "required":["args"]}
```

执行：`redis-cli -h 127.0.0.1 -p <port> [-a <password>] <args...>`

- `args` 用数组而非空格分隔字符串：值含空格时无歧义，且天然 argv 安全。
- 不加 `--no-auth-warning`：内网 redis-cli 常见 3.x/5.x（CentOS 7 自带 3.2），
  不识别该 flag 会直接报错；`-a` 仅在 stderr 留一行警告噪音（不含密码明文），
  兼容性优先。
- Risky 级：GET/CONFIG GET 等只读命令也会过确认门——特性而非负担（FLUSHALL 类
  必须有人盯）；同一轮多条 redis_cli 调用共享 Agent 循环的一次批量确认
  （ConfirmBatch），确认成本可摊薄。
- 密码参数对本次 fan-out 的全部主机一致；各主机凭据不同时，Agent 分主机逐次
  调用（hosts 传单机）。

### 4.3 redis_rdb（Safe，Targeting）

RDB 文件离线分析，产出键空间报告。

InputSchema：

```json
{"type":"object","properties":{
  "path":{"type":"string","description":"RDB file path on target host; omit to auto-detect from the live instance"},
  "port":{"type":"integer","description":"Port used for auto-detect (default 6379)"},
  "password":{"type":"string"},
  "top_n":{"type":"integer","description":"Top prefixes / bigkeys count (default 10)"},
  "force":{"type":"boolean","description":"Allow fetching files larger than 1GB (default false)"},
  "hosts":{...}}}
```

流程：

1. **路径解析**：显式 `path` 优先；否则 `redis-cli ... CONFIG GET dir` +
   `CONFIG GET dbfilename` 拼出（实例不存活时必须显式 path，报错提示）。
2. **大小守卫**：`stat -c '%s %Y'` 取字节数与 mtime（报告头展示用）；>1GB 且未
   `force:true` → 拒绝并提示（防 SFTP 拉爆带宽与本地磁盘）。
3. **取回**：`executor.FetchFile(ctx, remotePath, localTmp)` 流式落到本地临时文件
   （`os.CreateTemp`）。
4. **解析+聚合**：`rdbparser.Analyze(localTmp)`（§5）。
5. **报告**：渲染 ≤2KB 文本；`defer os.Remove(localTmp)`（RDB 含真实键值数据，
   成败都清理，不留盘）。

报告样例（内存数值注明估算）：

```
=== rdb: /var/lib/redis/dump.rdb (114MB, mtime 2026-10-30 03:00) ===
总键数 1,204,331 · 内存估算 812MB · DB0 98% / DB1 2%
类型: string 78% / zset 12% / hash 9% / list 1%
Top 前缀(按内存): session:* 412MB·512K键 | cache:user 210MB·301K键 | queue:job 88MB·190K键
永久键(无TTL): 45% (542K) —— ⚠️ 缓存类键建议设 TTL
Top 大键: zset user:rank 8.2MB | hash user:1001 6.1MB | string blob:42 5.5MB
(内存为估算值，算法同 rdr/MEMORY USAGE 近似)
```

多主机 fan-out：每主机独立拉取分析，Registry 按既有预算聚合截断。

## 5. rdbparser 包

分层：**解析核心（内置 rdr）→ 聚合层（自研）→ 渲染（自研）**。

### 5.1 解析核心

- 内置 xueqiu/rdr 的 RDB 解析核心（Apache-2.0，`LICENSE` 随包保留归属）；
  裁掉 web UI（go:embed 静态资源）与 CLI（urfave/cli）等我们不需要的部分。
- 产出逐键流（key / type / 字节数估算 / TTL / 所属 DB），供聚合层消费。
- **兼容性风险与对策**：rdr 是 2017 年工具（v0.0.1），Redis 6/7 新编码
  （listpack、stream）可能缺口。实施时建夹具矩阵（Redis 3.2/5/6/7 的 dump 各一，
  离线用真实 redis-server 生成一次后作为静态文件入库 testdata/——测试本身从不
  拉起 redis，与 §7「不做真实 Redis 集成」不冲突）；有缺口则在 vendored parser 上
  补编码支持（RDB 格式公开，listpack 是紧凑格式，改动可控）；补不动的编码先
  报「不支持该编码」跳过该键并计数，不废整份报告；解析核心实在不行则整体
  换 HDT3213/rdb（活跃维护的同用途纯 Go 库）——聚合层接口不变，替换成本隔离在解析层。

### 5.2 聚合层（自研，纯函数）

```go
// Analyze 流式读取 RDB 并聚合。keyStream 由解析核心供给。
func Analyze(path string) (*Report, error)

type Report struct {
    TotalKeys     int64
    EstBytes      int64
    DBs           map[int]DBStat   // 键数/内存估算
    Types         map[string]int64 // string/hash/list/set/zset/stream
    Prefixes      []PrefixStat     // 按内存与键数两个排序取 Top-N
    BigKeys       []KeyStat
    NoTTL, HasTTL int64
    Skipped       int64            // 不支持的编码跳过计数
}
```

- 前缀切分：key 按 `:` 取前两段（`session:user:1001` → `session:user`），
  单段键自成一组。
- 聚合与渲染均为纯函数，表驱动测试。

### 5.3 executor.FetchFile

```go
// FetchFile 流式取回远程文件到本地路径。Local 实现为复制；SSH 实现走
// pkg/sftp（已有依赖）逐块拷贝，不整读进内存。
FetchFile(ctx context.Context, remotePath, localPath string) error
```

## 6. 错误处理

| 场景 | 行为 |
|---|---|
| 目标机无 redis-cli | exec 报 command not found → IsError 透传，工具描述提示先装客户端 |
| NOAUTH / WRONGPASS | 错误原样透传，Agent 调整凭据重试（discover 已标注哪些端口需密码） |
| CONFIG GET 拼路径失败 / 文件不存在 | 明确报错；实例不存活时提示必须显式 path |
| 文件 >1GB 且未 force | 拒绝执行，错误信息说明 force 参数 |
| 解析失败 / 不支持的编码 | 单键跳过计数（Skipped）；文件级解析失败提示「RDB 版本可能不受支持，请反馈 Redis 版本」 |
| 临时文件 | defer 清理，成败皆删 |
| 多主机部分失败 | 沿用 Registry 聚合（`===== host =====`、[FAILED]、按主机截断），全部失败才算工具级错误 |

## 7. 测试策略

- **plugin 层**：fakeExec 表驱动——argv 构造正确性（含密码参数顺序）、discover 的
  pgrep/ss/PING/INFO 输出夹具解析、redis_rdb 的路径拼接/大小守卫/force 分支。
  覆盖 Local 与 SSH 两种 executor 的行为差异不需要（fake 一致）。
- **rdbparser**：手工构造最小 RDB 二进制夹具（REDIS 头 + 少量 string/intset/ziplist
  编码键 + EOF/CRC）走全链路；聚合层与渲染层纯函数测试用合成 key 流，不依赖二进制。
- **executor.FetchFile**：Local 实现单测（临时目录复制）；SSH 实现留 integration 档
  （现有 Docker sshd 框架）。
- **不做的测试**：真实 Redis 集成（本期明确范围外，见 §8）。

## 8. 明确不做（本期）

MySQL 全部工具、probe 探伤报告、bigkeys 在线采样、部署（Skill + config_gen）、
memory instances 分类、定期巡检调度、协议直连通道、SecretStore 凭据加固。

## 9. 后续演进路径（记录，不承诺）

1. **MySQL 插件**：按本模式重启（discover/probe/query/backup/slowlog，风险分级同型）。
2. **部署**：`/redis-deploy` Skill playbook + `redis_config_gen` 确定性配置生成工具
   （过程性知识进 Skill、原子能力进工具）。
3. **探伤沉淀**：redis_probe（INFO 各节 → ✅/⚠️/❌ 报告+阈值建议）从透传用法中
   沉淀为独立工具；探伤即「优化」维度的规则化落点。
4. **凭据加固**：memory → SecretStore（AES-GCM 已有）迁移；届时 redis_cli 的
   password 参数改为 secretRef。
5. **中间件共享层**：第三个中间件插件出现、重复逻辑可观时，再评估抽
   `internal/mw` 薄共享层（连接参数拼装、报告渲染等）。

---
psy_kind: factual
psy_version: 1
package: internal/rdbparser
---

# internal/rdbparser

## 概述

`internal/rdbparser` 提供 Redis RDB 文件的离线解析与键空间分析：内置开源解析核心（dongmx/rdb，rdr 同源）把 RDB 回调流转换为 `Entry` 通道，自研聚合层统计键数/内存估算/DB/类型/TTL/前缀 Top-N/大键，渲染层产出面向 LLM 的紧凑文本报告。`plugin` 包的 `redis_rdb` 工具消费 `Analyze`/`Render`。零 go.mod 依赖（解析源码以内部包形式内置，许可文件随目录保留）。

## 文件

- `README.md` — 来源说明：`rdb/` = dongmx/rdb@e1191ecb6823（tent/rdb 谱系）、memprofiler.go 与移植的 decoder.go = xueqiu/rdr master（Apache-2.0）、errors/ = 本地 shim；升级需重新比对上游
- `analyze.go` — `Analyze` 入口：goroutine 驱动 `rdb.Decode`（recover 转错误），主协程排空 Entry 通道后聚合
- `decoder.go` — rdr decoder 适配层移植：回调流 → `Entry` 通道（`DB`/`Expiry` 追踪、set 类型修正、未知编码跳过计数、`Fail`/`Err` 错误回传、幂等关闭）
- `memprofiler.go` — rdr 内存估算器（逐字节保留，仅改 package）
- `aggregate.go` / `aggregate_test.go` — 聚合层：`Report`/`PrefixOf`/aggregator（纯内存累计 + 排序截断 TopN + 冻结防护）
- `render.go` / `render_test.go` — 渲染层：`Render` 九节文本报告 + `HumanBytes`
- `analyze_test.go` — 端到端：真实 dump 夹具断言 + 缺文件/垃圾输入错误路径
- `testdata/` — 离线生成的真实 dump 夹具（redis 3.2 已入库；5/6.2/7.2 待 Docker 可用后补齐，测试按文件存在性显式 SKIP，断言不放宽）
- `rdb/` — vendored dongmx/rdb（decoder.go、slice_buffer.go、crc64/、nopdecoder/、LICENCE）：RDB 二进制格式解析核心，支持 listpack/stream/module
- `errors/errors.go` — juju/errors 兼容 shim（New/Errorf/Trace/Wrap，stdlib 实现，%w 保留 cause），保住 vendored 调用点与上游逐字节一致
- `LICENSE` — rdr 的 Apache-2.0（覆盖 memprofiler.go 与 decoder.go 移植）

## API

- `Analyze(path string, topN int) (*Report, error)` — 流式解析本地 RDB 文件并返回聚合报告；topN<=0 默认 10；解析失败/损坏返回错误（不 panic）
- `Render(path string, sizeBytes, mtimeUnix int64, r *Report) string` — 渲染报告（sizeBytes 预期非负，来自 stat 探测）；输出约 0.6-2KB
- `HumanBytes(n uint64) string` — 人类可读字节数（1 位小数，B/KB/MB/GB）
- `PrefixOf(key string) string` — 键前缀切分：按 `:` 取至多两段（`session:user:1001` → `session:user`；无分隔符自成一组）
- `type Report` — 聚合结果：RDBVersion/RedisVersion/DumpCtime/UsedMemSnapshot/TotalKeys/EstBytes/Skipped/DBs/Types/NoTTL/HasTTL/PrefixesByBytes/PrefixesByKeys/BigKeys
- `type DBStat` / `PrefixStat` / `KeyStat` — 分项统计结构
- `type Entry` — 单键解析结果（Key/DB/Expiry（unix 毫秒，0=永久）/Bytes/Type/NumOfElem/…）
- `type Decoder` + `NewDecoder()` — 回调适配器（rdb.Decoder 接口实现）；`Fail(err)` 生产方错误回传、`Err()` 读取、`RDBVersion()/RedisVersion()/Timestamp()/UsedMem()/SkippedCount()` 元数据访问器。**契约：一个实例只驱动一次 rdb.Decode；消费端必须排空 Entries**

## 依赖

**内部包：** `gengar-pro/internal/rdbparser/rdb`（vendored 解析核心）、`gengar-pro/internal/rdbparser/rdb/nopdecoder`、`gengar-pro/internal/rdbparser/errors`（shim）

**第三方库：** 无（go.mod 零新增；rdb/crc64 为 vendored 自带实现）

**标准库：** `bufio`、`fmt`、`os`、`sort`、`strconv`、`strings`、`sync`、`time`

## 设计重点

### vendored 分层与许可

解析核心不进 go.mod 而以 `internal/rdbparser/rdb/` 内置（上游 2018 年后未大动、快照即版本），dongmx/rdb 的 `juju/errors` 依赖用 38 行 stdlib shim 替代（`errors/`，包文档注明与 juju 的两处分歧）。两份许可文件按覆盖范围放置：`rdb/LICENCE`（dongmx 谱系）管子树，根 `LICENSE`（Apache-2.0）管 rdr 移植文件。来源与快照 SHA 见包 README。

### decoder 适配点（相对 rdr 母本）

八处适配，每处就地 `// 适配：` 注释标注：Entry 增 DB/Expiry（母本不追踪，报告需要）；`StartDatabase` 追踪当前 DB（注意接口方法名不是母本的 SelectDB）；`StartSet` 修正 Type（母本错记 hash）；未知编码不 panic 改跳过计数（失败不废整份报告）；`Aux` 抓 redis-ver；`Fail/Err` 错误回传（rdb.Decode 出错不触发 EndRDB）；去 stderr 噪音。内存估算算式与母本逐回调一致。

### skip 状态机与幂等关闭

容器键遇不支持编码时置 `skipCurrent` 并丢弃当前 Entry，后续元素回调早退、对应 `End*` 重置——每个键恰好计数一次 `Skipped`。通道关闭经 `closeOnce` 幂等（EndRDB 与 Fail 竞争不 double-close），`parseErr` 由 `failOnce` 保证恰写一次；可见性经通道 close 的 happens-before，无锁。

### 确定性输出

聚合与渲染的所有排序都是全序：主指标降序 + 唯一字符串字典序 tiebreak（前缀名/键名/类型名），map 迭代随机性不漏进结果，测试不抖动。aggregator `finish` 后再 `add` 会 panic（冻结防护，防共享指针静默改坏已返回的 Report）。

### 夹具矩阵现状（2026-10-31）

`testdata/` 持有离线生成的真实 dump（生成脚本与分析同源断言：6 键、TTL 5/1、五类型）。redis 3.2（本机 3.2.100 生成）已入库且全绿；5/6.2/7.2 因开发机 Docker Desktop 服务条款未接受而未生成，对应子测试按文件存在性显式 SKIP（断言原文保留，夹具入库后零改动解除）。3.2 的多字段 hash 需 `HMSET`（HSET 多字段是 4.0+ 语法）。已知限制：dongmx/rdb 在 EOF 操作码后直接返回，不校验尾部 CRC——EOF 之后的尾部损坏检测不到。

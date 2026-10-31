# rdbparser 来源说明

本包实现 RDB 内存分析（rdr 风格），全部源码本地内置，不引入 go.mod 外部依赖。

- `rdb/`：解析器，vendored 自 github.com/dongmx/rdb @ e1191ecb6823（2020-07-14，
  LICENCE 随目录保留，仅 import 路径重写）。上游 hdp/rdb 2018 年后未再大动，
  此 fork 是长期稳定的快照。
- `memprofiler.go` 与 `decoder.go`：移植自 github.com/xueqiu/rdr master
  （Apache-2.0，LICENSE 见本目录）；decoder.go 相对母本的适配点见其文件头注释
  （DB/TTL 追踪、set 类型修正、未知编码跳过计数、错误回传等）。
- `errors/`：本地 shim，替代 github.com/juju/errors（vendored 代码的 import
  路径已重写指向它），保持零依赖。

RDB 新类型/新编码的支持范围以这份 vendored 快照为准；若需支持更新的 RDB
版本，需重新拉取上游并逐项比对合并。

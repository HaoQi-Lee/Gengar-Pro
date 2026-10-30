---
psy_kind: factual
psy_version: 1
package: internal/xshell
---

# internal/xshell

## 概述

`internal/xshell` 解析 XShell 会话文件（`.xsh`，UTF-16LE BOM 编码的 INI 格式），产出可导入 gengar-pro 主机清单的结构化会话数据。双入口：`Scan` 递归扫描本机会话目录，`ParseFiles` 解析浏览器上传的文件内容（Web 形态主路径）。只读连接结构（Host/Port/UserName/UserKey），**不触碰 Password 键**——XShell 密码为专有加密，导入策略为结构导入、密码留空（设计决策 X1）。

## 文件

- `xshell.go` — `Scan`（递归 + 容错扫描）与 `ParseFiles`（上传解析）、`Session`/`Result`/`File` 数据结构、`parseFile`/`parseSession`/`parseINI`/`decode`（编码与 INI 解析）、`dirTags`（文件夹→标签）、`UniqueName`（导入重名加后缀）
- `xshell_test.go` — 11 个用例：正常解析/排序、缺省与跳过、UTF-8 无 BOM、密钥认证、UTF-8 BOM 兜底、value 含 `=`、空文件名、端口越界、目录不存在、UniqueName、ParseFiles 上传路径

## API

- `type Session struct` — 可导入会话：Name（文件名去 .xsh）、Host、User（缺省 root，与 HostForm 一致）、Port（缺省 22）、AuthMethod（"key"|"password"）、KeyPath（UserKey 原样带入）、Tags（子文件夹路径段）、Source（ToSlash 相对路径）
- `type Result struct` — 解析汇总：Sessions（按 Source 排序）、Skipped（非 SSH 协议或 Host 为空）、Failed（解析失败，容错继续）
- `type File struct` — 一次上传的单个 .xsh：Name（浏览器侧相对路径）+ Content（原始字节）
- `Scan(dir string) (*Result, error)` — 递归扫描 `.xsh`（扩展名不区分大小写）；WalkDir 错误（如目录不存在）整体报错，单文件失败计入 Failed 不中断
- `ParseFiles(files []File) (*Result, error)` — 解析上传文件列表（容错语义与 Scan 一致：单文件跳过/失败不打断整批）
- `UniqueName(base string, used map[string]bool) string` — 重名时追加 -2/-3…（store 的 name 列有 UNIQUE 约束；XShell 子文件夹同名会话常见，webapi 预览端点用它给预览行分配最终名）

## 依赖

**第三方库：** 无（纯标准库）

**标准库：** `errors`、`fmt`、`io/fs`、`os`、`path/filepath`、`sort`、`strconv`、`strings`、`unicode/utf16`

## 设计重点

### 编码与容错三层兜底

主格式 UTF-16LE（BOM FF FE）经标准库 `unicode/utf16` 解码（无新依赖）；无 BOM 按 UTF-8 容错并剥离 UTF-8 BOM（EF BB BF，Windows 第三方工具常见形态）；解析出的坏内容归入 Failed 计数而非中断——会话目录里混着无关/损坏文件是常态，整批导入不应被单个文件打崩。

### Skipped 与 Failed 的语义边界

有 `Protocol` 键但非 SSH（Telnet/Serial）或 Host 为空 → Skipped（合法但不适用的会话文件）；完全无 `Protocol` 键（编码损坏/垃圾内容）→ Failed。预览界面分别展示两类计数（webapi 上传解析端点回传 `skipped`/`failed`），解释 .xsh 文件数与可导入行数的差。

### 目录扫描与上传解析双入口

`Scan` 与 `ParseFiles` 共用 `parseSession`（单文件内容解析）与 `dirTags`/排序逻辑：前者服务端读盘（相对路径自扫描目录），后者收浏览器上传的原始字节（相对路径来自 webkitdirectory 的层级，webapi 剥根目录后传入）；Tags/folder 映射在两入口下一致。新增解析行为只落 `parseSession` 一处，两入口同步生效。

### 安全边界

解析器只读五个键（Protocol/Host/Port/UserName/UserKey）；`Password` 键在代码中不存在任何读取路径（安全审计点），密钥路径原样透传不解析——NetSarang 自有密钥格式未必兼容 Go SSH，连通失败由用户在编辑表单改路径。

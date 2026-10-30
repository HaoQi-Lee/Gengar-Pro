# gengar-pro 设计文档：XShell 会话导入

- 日期：2026-10-21
- 状态：已评审通过（密码策略=结构导入、交互=预览勾选，经用户确认；探索基于本机真实数据 XShell 6/7/8）
- 前置：`.psy/2026-10-21/2026-10-20-gengar-pro-desktop-design.md`（D4 将导入类功能列为后续版本——本设计兑现其一）

## 1. 背景与目标

用户本机长期使用 XShell（6/7/8 三代共存，分别 38/26/33 个会话），手工把会话逐台录入 gengar-pro 成本高。目标：从本机 XShell 会话目录一键批量导入主机清单。

**探索确认的格式事实**（本机实测）：

- 会话文件：`%USERPROFILE%\Documents\NetSarang Computer\{6,7,8}\Xshell\Sessions\` 下的 `.xsh` 文件，**UTF-16LE（BOM）编码的 INI 格式**
- 关键字段：`[CONNECTION]` 节的 `Host`、`Port`、`Protocol`（SSH/Telnet/…）；`[CONNECTION:AUTHORIZATION]` 节的 `UserName`、`UserKey`（密钥路径，空=密码认证）、`Password`（专有加密）
- 会话名 = 文件名去 `.xsh`；子文件夹（如 `minio/`、`xlight/`）是会话分组
- 本机 v8 的 33 个会话全部为密码认证（UserKey 为空）

## 2. 决策记录

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| X1 | 密码策略 | **结构导入**：Host/Port/User/名称/文件夹→标签；密码一律留空，导入后经 ✎ 逐台补录 | 解密 XShell 专有密码：算法随版本变化、可能解不出、逆向自有加密风险高；用户确认放弃 |
| X2 | 交互形态 | **预览勾选**：扫描 → 弹窗列出会话（重复项标记且默认不勾）→ 勾选 → 批量导入 | 一键全量：33 台里总有不想要的，事后清理更差 |
| X3 | 解析位置 | **Go 包 `internal/xshell`**（纯解析、可 TDD、headless 可复用） | 前端 TS 解析（Wails 前端无文件系统访问）；headless 命令行（无预览交互） |
| X4 | 入口 | 侧栏操作区「导入」按钮（与 + 添加/设置并列，busy 禁用） | 设置页内（埋得深，导入是一次性高频意愿动作） |

## 3. 设计

### 3.1 Go：`internal/xshell` 纯解析包

```go
type Session struct {
    Name       string   // 文件名去 .xsh
    Host, User string
    Port       int      // INI 缺省 22
    AuthMethod string   // "key" | "password"（UserKey 非空 → key，KeyPath 原样带入）
    KeyPath    string
    Tags       []string // 子文件夹路径段（根目录无标签）
    Source     string   // 相对路径（预览展示与排错）
}

type Result struct {
    Sessions []Session // 仅 Protocol=SSH 的有效会话
    Skipped  int       // 非 SSH 协议（Telnet/Serial 等）
    Failed   int       // 解析失败的文件（容错继续）
}

func Scan(dir string) (*Result, error) // 递归遍历 *.xsh
```

- **解码**：BOM `FF FE` → `unicode/utf16` 标准库解码；无 BOM 按 UTF-8 容错读取（覆盖个别非标准文件）
- **INI 解析**：手写极简解析（节跟踪 + key=value），只取上述字段，未知节/键忽略
- **容错**：单文件解析失败计入 `Failed` 不中断整体；`Host` 为空的会话视为无效计入 `Skipped`

### 3.2 绑定（app.go）

- `DetectXshellDirs() []XshellDirView` — 探测标准路径 `{6,7,8}`，逐个 `Scan` 计数，返回 `[{Label:"XShell 8", Path, Count:33}]`（不存在/无会话的目录不返回）
- `ScanXshell(dir string) (XshellScanView, error)` — `XshellScanView{Rows []XshellRowView; Skipped, Failed int}`：`xshell.Scan` + 对照现有清单标记重复（`Duplicate bool`；判重键 = addr+port+user，设计选择）+ 重名自动加 -2/-3 后缀（预览行显示最终名；store 的 name 有 UNIQUE 约束）；跳过/失败计数随行返回供预览提示
- `BrowseXshellDir() (string, error)` — 后端 `runtime.OpenDirectoryDialog`（前端无文件系统访问）
- `ImportXshell(reqs []HostReq) error` — 逐台复用 `SaveHost` 语义入库（req 由前端从勾选行构造，密码恒空）；首错中止并返回

### 3.3 前端：`ImportDialog` + 侧栏入口

- Sidebar 操作区加「导入」按钮（busy 禁用）→ App 打开 `ImportDialog`
- 弹窗布局：顶部目录下拉（`DetectXshellDirs` 数据）+「浏览…」（`BrowseXshellDir`）→ 变更即 `ScanXshell` 刷新表格；表格列：勾选框 / 名称 / `地址:端口` / 用户 / 标签 / 来源；重复行显示「已存在」徽标且勾选框禁用（无覆盖语义，见 §6）；底部「全选/清空」+「导入 N 台」
- 导入成功 → toast「已导入 N 台 · 密码需在 ✎ 补录后才能连通」+ 刷新侧栏 + 关闭弹窗
- 解析计数（跳过/失败）在表格上方以一行小字展示

### 3.4 数据与安全

- 密码字段全程不读取、不传输（解析器根本不触碰 `Password` 键）
- 导入主机 = 普通 Host 记录，无任何 XShell 来源标记残留（Source 仅用于预览，不入库）
- 密钥认证会话：KeyPath 指向 XShell 的 UserKey 文件（NetSarang 自有格式），Go SSH 未必能解析——测试连通失败属预期，✎ 可改路径（本机数据无此类会话，设计上不拦）

## 4. 错误处理

| 场景 | 行为 |
|---|---|
| 标准路径无任何 XShell 目录 | 下拉为空 + 提示「未探测到 XShell，可手动选择目录」 |
| 自选目录无 .xsh | 表格空 + 计数行显示 0 |
| 单文件解析失败 | 计入 Failed，整体继续 |
| ImportXshell 中途失败 | 立即返回错误（已入库的保留），toast 报告 |
| 目录选择框取消 | 返回空串，前端忽略 |

## 5. 测试策略

- **Go（`internal/xshell`）**：表驱动单测——测试内写临时 `.xsh`（UTF-16LE 编码由测试构造）：正常解析/端口缺省/非 SSH 跳过/Host 空跳过/子文件夹标签/根目录无标签/坏文件容错/UTF-8 无 BOM 容错
- **绑定层**：薄封装不单测（判重键与 HostForm 一致，逻辑在 Scan/store 侧）
- **前端**：弹窗交互手动验证（无新纯函数）

## 6. 明确不做（本期 YAGNI）

- XShell 密码解密
- XShell UserKey（NetSarang 格式）转 OpenSSH 格式
- 密钥口令/代理设置的导入（gengar-pro 尚无对应字段）
- PuTTY/SecureCRT/FinalShell 等其他工具导入
- 重复导入合并/覆盖策略（重复行仅禁选）

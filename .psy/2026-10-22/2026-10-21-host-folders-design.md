# gengar-pro 设计文档：侧栏目录树主机管理

- 日期：2026-10-21
- 状态：按用户委托的推荐方案执行（目录树 + 隐式目录 + 表单设目录；预授权连续开发）
- 前置：主机编辑 / XShell 导入（`.psy/2026-10-21/` 已归档两设计）

## 1. 背景与目标

侧栏主机清单是平铺列表，机器多了以后无组织手段（XShell 用户习惯按目录分组管理会话）。目标：侧栏支持**目录树**组织主机，进出目录（展开/收起）顺手，与既有交互（钉住/终端/编辑/删除/状态点）不冲突。

## 2. 决策记录

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| F1 | 目录数据模型 | **Host 增 `Folder string` 字段**（斜杠路径 "生产/web"，空=根；目录由主机派生，无独立目录实体、无空目录） | 独立目录表（alpha 过度设计）；复用 Tags 充当目录（混淆两种语义：标签喂 Agent 目标解析，目录纯 UI 组织） |
| F2 | 进出目录交互 | **XShell 式树**：目录行整行可点展开/收起（▾/▸ + 名称 + 子项计数），主机行按深度缩进；默认全展开；展开态会话内存态 | 面包屑逐级下钻（多两次点击）；默认全收起（钉住主机易"失踪"） |
| F3 | 目录的设置方式 | **HostForm「目录」文本字段**（"生产/web"；留空为根）+ XShell 导入直接填目录 | 拖拽移动（v1 不做，YAGNI）；目录右键菜单管理（无空目录实体，无从管理） |
| F4 | 折叠目录的钉住可见性 | 目录行显示**递归钉住计数徽标**（📌N），折叠也不丢钉住线索 | 忽略（折叠后钉住主机失踪）；强制展开含钉住的目录（破坏用户折叠意愿） |
| F5 | 目录与 Agent 目标解析 | **目录不参与目标解析**（标签语义不变）；文档明确 | 目录名也可作为目标（语义重叠，后续按需） |

## 3. 设计

### 3.1 数据层（internal/host）

- `Host.Folder string`（json `folder`）；`Store` schema 增 `folder TEXT NOT NULL DEFAULT ''`
- **迁移**：`CREATE TABLE IF NOT EXISTS` 不改旧库——打开时 `ALTER TABLE hosts ADD COLUMN folder TEXT NOT NULL DEFAULT ''`，列已存在（`duplicate column name`）则忽略；幂等
- **规范化**（Save 前统一）：`NormalizeFolder(s)`——trim 空白与首尾 `/`，段内 trim，`//` 折叠，空段丢弃；结果存库；导出供 app.go 复用
- Save/List 读写 folder；UNIQUE 语义不变（name 仍唯一）

### 3.2 绑定层（app.go）

- `HostReq` 增 `Folder`；`SaveHost` 保存前 `host.NormalizeFolder`（前后端双防护，前端仅提示不做权威）
- `HostView` 增 `Folder`（侧栏树与编辑预填用）

### 3.3 侧栏树（frontend）

- **纯函数模块 `src/folders.ts`**：`buildTree(hosts): FolderNode[]`——按 Folder 路径分组，构造有序树（根主机在前、目录按名排序，目录含 `hosts` 与递归 `pinnedCount/childCount`）；vitest 覆盖（空清单/根主机/嵌套路径/排序/钉住计数）
- Sidebar 渲染：目录行（▾/▸ 整行点击切换 + 📁 名 + 递归计数 + 📌N 徽标）；主机行缩进 `depth*14px`，其余控件（状态点/钉住/>_/✎/删除）原样
- 展开态：`useState<Set<string>>`（路径为键），**默认全展开**（buildTree 后初始集合）；会话内存态
- HostForm：「目录」字段（标签之前，placeholder `生产/web · 留空为根目录`），编辑预填，提交带 folder
- types.ts：HostView/HostReq 增 folder

### 3.4 XShell 导入联动

- `XshellRowView` 增 `Folder`（= Source 的目录部分，斜杠路径；根会话为空）
- ImportDialog 构造 HostReq 时带 `folder`（导入即归位目录）；tags 行为不变（目标解析语义保留）

## 4. 错误处理

| 场景 | 行为 |
|---|---|
| 旧库无 folder 列 | ALTER 迁移，已有行默认 ''（根目录），透明 |
| Folder 非法输入（"//a// b //"） | NormalizeFolder 归一为 "a/b"；空串=根 |
| 删除某目录最后一个主机 | 目录随派生源消失（树自动收敛），无残留 |
| 折叠目录中的钉住主机 | 📌N 徽标提示；Agent 目标解析不受折叠影响 |

## 5. 测试策略

- **Go（internal/host）**：迁移幂等（模拟旧库→新库重复打开）、folder 读写往返、NormalizeFolder 表驱动（含非法输入）
- **前端（src/folders.ts）**：buildTree 纯函数单测（分组/排序/嵌套/钉住计数/空）
- 手动清单：树渲染与展开收起、缩进、表单目录保存、导入归位、旧数据升级

## 6. 明确不做（本期 YAGNI）

- 拖拽移动主机到目录
- 目录重命名/整体移动（改每台主机的 folder 即可，批量操作后续）
- 空目录的创建与持久化
- 目录作为 Agent 目标解析键
- 目录内搜索/过滤

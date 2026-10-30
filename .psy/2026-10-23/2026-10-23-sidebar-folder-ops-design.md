# 侧栏目录操作（右键菜单 + 主机拖拽） 设计 v2

日期：2026-10-23 · 分支：feature/web-system

## 决策

**单级目录 + 目录保持虚拟**（随主机存在）：不引入空目录注册表、不加后端端点——「新建目录」直接弹出预填该目录的添加主机表单，目录随第一台主机诞生；取消表单 = 目录从未存在。「删除目录」= 目录内全部主机移回根，目录自动消失。嵌套渲染代码保留（兼容存量多级路径），新建入口只产生单段名（含 `/` 拒绝）。

## 交互

| 操作 | 行为 |
|---|---|
| 右键空白处（host-list） | 菜单：新建目录（小输入弹窗 → 校验非空/无 `/`/不重名 → 打开预填该目录的 HostForm）· 添加主机（根） |
| 右键目录行 | 菜单：添加主机到此目录（HostForm 预填 n.path）· 删除目录 → 行内二次确认（「确认移出全部主机？」✓/×，同主机删除防误触精神）→ 目录内主机 folder 全部置空 |
| 拖拽主机 → 目录行 | dragover 高亮、drop → SaveHost 改 folder（同目录 no-op）；成功后目标目录自动展开 |
| 拖拽主机 → 空白区 | folder="" 移回根 |

拖拽只改 folder 字段——服务端编辑路径本就保留秘密引用与 KeyPath，密码/密钥不受影响。

## 实现面（全前端）

- `Sidebar.tsx`：自绘右键菜单（fixed 定位，外部点击/Escape 关闭，TerminalPane 同款先例）、FolderPrompt 小弹窗、目录删除确认态、HTML5 DnD（host 行 draggable；目录行 stopPropagation drop；host-list 容器 = 根放置区）
- `App.tsx`：`presetFolder` 状态（addHost 带目录参数，HostForm key 含 preset 强制重挂载）、`moveHost(hostId, folder)`（HostView→HostReq 构造、SaveHost、刷新）、`deleteFolder(folder)`（逐台置空 + toast 汇总）
- `HostForm.tsx`：`presetFolder` prop（仅新建时初始目录值）
- `styles.css`：`.ctx-menu`、`.folder-row.drop-target`、`.host-row.dragging`、目录确认态样式

## 测试

组件交互按项目边界人工验收；`npm test` + `tsc` + `build` 全绿 + 二进制重建。

## 非目标

子目录创建、目录重命名、拖拽目录本体、拖到折叠目录悬停自动展开。

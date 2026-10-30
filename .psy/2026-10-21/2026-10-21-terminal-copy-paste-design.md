# gengar-pro 设计文档：终端右键菜单与复制粘贴（对标 XShell · 第一步）

- 日期：2026-10-21
- 状态：已评审通过（范围经用户确认：先做复制粘贴）
- 前置：内嵌终端（`.psy/2026-10-21/2026-10-21-host-edit-chat-timing-terminal-design.md` §5.3）

## 1. 背景与目标

内嵌终端（TerminalPane / xterm.js）中右键无菜单（WebView 吞掉默认菜单、xterm 不带剪贴板键位），复制粘贴完全不可用——运维终端的高频操作。对标 XShell 的第一步：右键菜单 + 复制粘贴 + 习惯键位。

## 2. 决策记录

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| C1 | 剪贴板通道 | **Wails 运行时 `ClipboardSetText/GetText`** | navigator.clipboard：WebView2 读取有权限弹窗风险；系统级 API 稳定直读 |
| C2 | 右键形态 | **自绘菜单（复制/粘贴）**，结构留扩展位 | 纯右键粘贴（无菜单）：与「对标 XShell 菜单」诉求不符 |
| C3 | 键位 | Ctrl+Insert / Ctrl+Shift+C 复制；Shift+Insert / Ctrl+Shift+V 粘贴 | 覆盖 XShell 与通用终端双习惯 |
| C4 | 复制后选区 | 保留（XShell 行为） | 立即清除易误判复制是否成功 |

## 3. 设计

- **右键菜单**：pane 容器 `onContextMenu` → preventDefault → 记录 `{x, y}` 开菜单；「复制」仅在有选区时可用（`term.hasSelection()`，开菜单时快照）；「粘贴」恒可用。点击外部 / Esc / 窗口失焦关闭；坐标按窗口尺寸钳制
- **复制**：`ClipboardSetText(term.getSelection())` → 保留选区
- **粘贴**：`ClipboardGetText()` → `term.paste(text)`（与击键同路径 onData → TerminalWrite 上行；断线后自然失败被既有吞错逻辑处理）
- **键位**：`term.attachCustomKeyEventHandler`——Ctrl+Insert / Ctrl+Shift+C → 复制（无选区时为无操作，**无条件拦截**不进输入流——Windows Terminal/XShell 惯例，避免误发 SIGINT；实际复制在点击时重查选区）；Shift+Insert / Ctrl+Shift+V → 粘贴并 return false
- 改动面：仅 `TerminalPane.tsx` + `styles.css`，无新依赖、无绑定变更

## 4. 错误处理

| 场景 | 行为 |
|---|---|
| 剪贴板读/写失败（被占用等） | 静默失败（.catch 吞），菜单项无反馈即可（系统剪贴板冲突是暂态） |
| 菜单打开时切换 tab/关窗 | 菜单随组件卸载消失；Esc/失焦兜底 |
| 粘贴大文本 | xterm onData 原样上行（服务端 PTY 接收；与键盘逐字输入同语义） |

## 5. 测试策略

- 无新纯函数（菜单状态为组件局部 UI 态）——构建门 + 手动验证清单（右键菜单出现/位置钳制、复制粘贴往返、四个键位、断线面板行为）
- 沿用「GUI 不写 E2E」原则

## 6. 明确不做（本期 YAGNI，菜单留扩展位）

- 全选/清屏/查找/打开 URL/发送 ASCII（XShell 菜单其余项）
- 复制时去除尾部换行/trim 选项
- 拖放文本进终端

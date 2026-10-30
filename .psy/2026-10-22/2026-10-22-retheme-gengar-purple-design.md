# gengar-pro 设计文档：应用视觉换肤——耿鬼紫夜（方向 B）

- 日期：2026-10-22
- 状态：已评审通过（三方向 mockup 经用户选定 B；共通落地项随 mockup 页一并展示）
- 参考：`site/assets/css/tokens.css`（CLI 官网本体主题）、`site/assets/svg/{grid-bg.svg,sprites/gengar-pixel.svg}`

## 1. 目标

桌面应用视觉与官网（CLI 本体）统一：深紫底 + 紫光主色 + 霓虹青点缀 + 网格图案 + 像素耿鬼角标，替换当前的中性灰主题。

## 2. 设计令牌映射（site → app styles.css）

| app 变量 | 新值 | 来源 |
|---|---|---|
| `--bg` | `#1a002e` | `--color-bg-gengar` |
| `--panel` | `#230840`（侧栏 `rgba(16,0,32,0.8)` 叠加网格） | 派生 |
| `--panel-2` | `#2d0d4d` | 派生 |
| `--border` | `#38155c` | 紫光 20% 描边的实色等价 |
| `--border-strong` | `#4a1d77` | 同上 |
| `--primary` | `#9d4dff` | `--color-gengar-glow` |
| `--success` | `#39ff7a` | `--color-gengar-accent` |
| `--danger` | `#ff2e88` | `--color-neon-pink` |
| `--text` | `#e6dcff` | `--color-text-primary` 的暖紫白 |
| `--text-dim` | `#b9a8e0`、`--text-faint` | `#8a77b3` | 派生 |
| `--mono` | `'JetBrains Mono', ui-monospace, ...`（本机无则回退，不引 webfont） | `--font-mono` |

霓虹青 `#00f0ff` 不入全局变量：仅作终端 tab 激活态与个别强调的硬编码点缀（对应 mockup）。

## 3. 图案与形状语言

- **背景**（`.app` 根）：`radial-gradient(ellipse 60% 50% at 75% 10%, rgba(157,77,255,.16), transparent)` + 两轴 80px 网格 `rgba(224,224,255,.045)`；面板半透明叠加（侧栏/对话区透出网格）
- **药丸圆角**：`.btn`/`.tag`/`.host-badge`/徽标类 → `border-radius: 999px`（官网 `--radius-pill`）；对话框/卡片保持 8-10px
- **辉光**：primary 按钮与焦点态 `box-shadow: 0 0 10px rgba(157,77,255,.35)`；思考指示器/残留行用紫光色 + `text-shadow` 微辉
- **像素耿鬼角标**：`PixelGengar.tsx`（内联官网 `gengar-pixel.svg` 的 16×16 rect 网格，`image-rendering: pixelated` 放大渲染）——放侧栏「主机」标题旁（16px）
- **耿鬼 logo（完整形象）**：源 `site/assets/img/gengar.png`（475×475 RGBA），三处使用：
  1. **应用图标**：`build/appicon.png` 重制为 512×512（源图缩放）；`build/windows/icon.ico` 重生成为多尺寸 PNG-entry ICO（256/128/64/48/32/16）——窗口标题栏/任务栏呈现耿鬼
  2. **侧栏品牌行**：logo 24px + 字标 `GENGAR PRO`（等宽字体、letter-spacing 宽），置于侧栏顶部（原「主机」小标题上方）
  3. **对话流空状态**：logo 96px 替代纯文字标题
  前端副本 `frontend/src/assets/gengar-logo.png`（Vite import），窗体图标用 build/ 产物

## 4. 组件适配

- `TerminalPane.tsx`：xterm `theme.background` → `#0d001f`
- `Chat.tsx` 空状态：标题换像素耿鬼图 + 保留文案
- `Sidebar.tsx`：标题行加角标
- `styles.css`：变量替换 + 背景图案 + 药丸/辉光 + 个别硬编码色（tool-dur.live 琥珀改紫光系 `#c9a5ff`、term-exit 粉、toast 面板色等）随之对齐
- xterm 光标/选区色走默认（背景已换）

## 5. 不做（YAGNI）

- JetBrains Mono webfont 引入（回退链即可）
- 窗口标题栏/vibrancy 等 Wails 窗口级改造
- site 的 Orbitron 展示字体（应用无 hero 场景）
- 动效曲线/视差等官网特效

## 6. 验证

构建门（tsc/vite + go/wails build）+ 手动清单：整体色调、网格透出、药丸按钮、辉光焦点、像素角标两处、终端新底色、弹窗/确认框/导入弹窗/目录树在新色下的可读性（对比度抽查）。

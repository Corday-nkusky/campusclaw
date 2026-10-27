# Design: notion-style-ui

现状盘点（本次审查的实际代码，不是推测）：

| 位置 | 现状 | 处置 |
|---|---|---|
| `frontend/src/styles.css`（1231 行） | `:root` 有 16 个 Token + `@media (prefers-color-scheme: dark)` 覆盖 13 个；正文按分区堆 class：布局 / 登录页 / 头部 / 面板 / 材料区 / Markdown / 导航 / 视图切换 / 命令面板 / 弹窗 / Toast / `kn-*` 知识页 | Token 层重定，正文分区逐段改 |
| `frontend/src/App.tsx`（286 行） | `.app-header` 单行 flex：brand / nav / spacer / 命令按钮 / `.who` / 退出 | 结构保留，样式重做（56px + hairline） |
| 四个页面 + 三个组件 | 类名沿用上面这套；**`.tsx` 里已无硬编码颜色**（`grep` 实测为空） | 只调类名与少量结构，不动逻辑 |
| `scripts/*.mjs` | 依赖一批选择器与一句文案做断言 | **冻结**，见 D9 |

## D1 沿用现有样式机制，不换方案

继续单文件 `styles.css` + CSS 变量 + `prefers-color-scheme`。不引 Tailwind / UI 库 / CSS-in-JS。
理由：本次是视觉升级，换样式方案会把「改了什么」淹没在迁移里，且 `package.json`、构建链路都要动。

## D2 中性色为主，品牌红只做点缀

`--brand: #94070a`（北大红）与 `--brand-fg` **原值保留**，但出现场合收敛为**四类点缀**：
主按钮底色、正文链接、焦点环、选中/激活指示。禁止把品牌色作为大块背景铺面
（现状 `.seg-btn.active` 是整块红底 —— 改为 `--surface-3` 背景 + 字重 600 + 品牌色下边条）。

## D3 深色底色锁定 #16161a（脚本契约，不是审美选择）

`scripts/browser-verify.mjs` B20 断言：

```js
check('B20', '切到 prefers-color-scheme: dark 后页面底色确实变化', darkBg === 'rgb(22, 22, 26)', …)
```

`rgb(22,22,26)` = `#16161a`，是 `--bg` 的深色值，写死在脚本里。所以**深色 `--bg` 本次不改**。
Notion 深色底色是 `#191919`，差 3 个色阶，肉眼几乎不可分 —— 层次改由 `--surface` /
`--surface-2` / `--surface-3` / `--border` 拉开，不靠底色。（若确实要改，必须同步改 B20 的期望字符串，
断言数不变；本变更按不改为默认路径。）

## D4 去卡片化：阴影只给浮层

建立三级层级，替代现状「所有内容块都是带阴影的卡片」：

| 层级 | 用在哪 | 表达 |
|---|---|---|
| 底 | `body` / `.page` | `--bg` |
| 内容块 | `.panel` / `.detail-box` / `.kn-hit` | `--surface`（比 `--bg` 亮一档）+ **hairline `--border`**，**无阴影** |
| 浮层 | `.palette` / `.dialog` / `.toast` / `.kn-modal` | `--surface` + `--shadow-float` |

圆角从 14px 降到 6px（内容块）/ 10px（浮层）；边框 1px hairline。
`.panel` 之间靠 `--sp-*` 间距与一条分隔线组织，不再靠「一个白盒子套一个白盒子」。

## D5 排版层级与行宽

字号与行高（Token 化，页面里不再出现裸 `font-size` 数值）：

| Token | 值 | 用途 |
|---|---|---|
| `--fs-12` | 12px / 1.5 | 徽标、脚注、超密元数据 |
| `--fs-13` | 13px / 1.55 | 元数据、面板小标题、菜单项 |
| `--fs-14` | 14px / 1.6 | 列表标题、表格、表单控件文字 |
| `--fs-15` | 15px / **1.7** | **正文与表单默认**（中文行高放宽） |
| `--fs-17` | 17px / 1.5 | 分区标题 `h3` |
| `--fs-20` | 20px / 1.4 | 页面标题 `h2` |
| `--fs-24` | 24px / 1.3 | 登录页 `h1` / 工作台主标题 |

字重只用 400 / 500 / 600 三档；标题靠**字号 + 字重 + 上方留白**分层，颜色只做辅助
（`--text` / `--text-muted` / `--text-faint` 三档）。

行宽：`--w-read: 880px`（阅读型：登录卡之外的内容、Markdown 正文、知识页答案）、
`--w-wide: 1040px`（数据型：材料列表、命中列表）；`margin-inline: auto`。
登录卡仍窄（`--w-login: 360px`），但去掉厚重阴影、圆角降到 8px。

中文字体栈沿用系统栈（`-apple-system / Segoe UI / PingFang SC / Hiragino Sans GB /
Microsoft YaHei`），不加载 Web Font。数字用 `font-variant-numeric: tabular-nums`
（沿用 `kn-score` 已有做法，推广到材料大小、时间、计数）。

## D6 间距与控件尺寸统一

间距 Token 全部取 4 的倍数：`--sp-1:4 --sp-2:8 --sp-3:12 --sp-4:16 --sp-5:20 --sp-6:24
--sp-8:32 --sp-10:40 --sp-12:48`。页面左右留白 `--gutter: 24px`（窄屏降 16px）。

控件：`--ctl-h: 36px`（按钮与输入框同高）、`--ctl-h-sm: 32px`（次要/密集区）、
`--r-sm: 4px`（控件）、`--r-md: 6px`（内容块）、`--r-lg: 10px`（浮层）。
现状按钮内边距与高度各写各的（`.btn-primary` / `.btn-ghost` / `.btn-secondary` 三套值）→ 统一。

## D7 五态齐全 + 焦点可见

每个可交互元素（`button` / `input` / `select` / `.nav-tab` / `.seg-btn` / `.material-title` /
`.palette-item` / `.kn-hit` / 弹窗按钮）都要有：hover（背景 `--surface-2`，**不位移**）、
focus-visible、active、disabled（`opacity` + `cursor: not-allowed`，且保留可辨识的边框）、
loading（按钮内文案改为「…中」，配合 `disabled`）。

焦点环统一：

```css
:focus-visible { outline: 2px solid var(--brand); outline-offset: 2px; }
```

**不移除任何焦点提示**（包括为了让导航看起来更干净而去掉 outline —— 那是明确禁止的）。
`.sr-only` / `aria-*` 现状保留不动。

## D8 动效轻、短、可关

`--t-fast: 120ms`、`--t-base: 160ms`，缓动 `cubic-bezier(0.2, 0, 0, 1)`。
只用于背景色、边框色、透明度的过渡；不做位移、缩放、弹跳。
`@media (prefers-reduced-motion: reduce) { *, *::before, *::after { transition: none !important; animation: none !important; } }`
（`kn-skeleton` / `kn-thinking` / `kn-dots` 三个 CSS 动画一并停掉）。

## D9 DOM 契约冻结（红线）

以下名字与文案**一个字符都不能改**，改了 `verify-all.sh` 就红：

- 选择器：`#username`、`#password`、`form`、`button[type="submit"]`、`#recheck-session`
- 类：`.card`、`.card h1`、`.alert`、`.alert-danger`、`.alert-notice`、`.badge`、`.who`、
  `.who .name`、`.who .muted`、`.page`、`.page h2`、`.page-lead`、`.app-nav`、`.nav-tab`、
  `.material-list`、`.material-item`、`.material-title`、`.material-meta`、`.material-grid`、
  `.material-card`、`.placeholder`、`.kv`、`.kv dt`、`.kv dd`、`pre.raw`、`.dev-notes`、
  `.search-bar`、`.input`、`.segmented`、`.seg-btn`、`.panel`、`.panel-head`、`.panel-body`、
  `.markdown-body`、`.palette`、`.palette-item`、`.dialog`、`.dropzone`、`.progress`、
  `.toast-host`、`.toast`、`.kn-*`（全部）
- 文案：学生能力描述必须是「**只能查看与下载**」（`e2e-acceptance.mjs` C1 从 `.kv dd` 里找这句）
- 值：深色 `--bg` = `#16161a`（B20）

可以做的：加新类、改结构嵌套（只要上述选择器仍能命中）、删掉纯装饰节点。

## D10 硬编码颜色规则

- `frontend/src/**/*.tsx`：**不得**出现 `#xxx` / `rgb(` / `hsl(`（现状已为零，保持）。
- `frontend/src/styles.css`：只有 `:root` 与 `@media (prefers-color-scheme: dark)` 两个**定义区**
  允许出现颜色字面量；正文区一律 `var(--*)`。
- 唯一的例外是定义区里的阴影 `rgb(...)` 透明度分量 —— 它本来就在定义区。
- 验收：`grep` 上述两条路径（命令写进 tasks）。

## D11 不引入图标库

项目零图标依赖。本次若需图标（如列表/网格切换、下载、关闭），用内联 SVG，
统一 `stroke-width: 1.6`、`currentColor`、16×16；**不混风格、不用 Emoji 冒充图标**。
现有纯文字按钮（下载 / 收起 / 刷新）可以保持纯文字 —— 不做无谓替换。

## D12 验证策略（判据是退出码与截图）

1. `npm run typecheck` + `npm run build`（tsc）零错误。
2. `bash scripts/verify-all.sh` 连续两轮退出码 0，各脚本断言数与基线一致
   （auth 21 / isolation 25 / upload 40 / retrieval 21 / health 19 / compose 17 / browser-verify 19 / e2e 31）。
   本变更**不新增脚本断言**。
3. 深浅两套主题 × 六态截图（CDP `Emulation.setEmulatedMedia` 切 `prefers-color-scheme`），
   存 `.verify-logs/evidence-notion-style-ui/` 并写 `manifest.json`。
4. 窄屏 768 / 414 各截一次，肉眼确认无横向溢出（另用 `scrollWidth > clientWidth` 判定）。
5. `bash scripts/check-docs-freshness.sh` 不报新过期；`docs/课程进度与后续路线.md` 回写本次状态。

## 不做（Non-goals）

- 不改 API、数据结构、路由、状态流转、业务逻辑；不改 `backend/` 一行。
- 不改 `openspec/specs/`（无 delta，理由见 proposal）。
- 不新增/删除脚本断言；不改 `scripts/` 下任何断言逻辑（B20 的值本次根本不动，见 D3）。
- 不加新功能、新页面、新路由；不做深色模式手动开关（那是行为变更）。
- 不引入任何新依赖（含字体、图标库、动画库）。
- 不为统一而强行把所有页面做成同一布局：阅读型（知识页答案、Markdown 正文）走窄栏纵向流，
  数据型（材料列表、命中列表）走宽栏。

# 任务：notion-style-ui

> **实施完成批注（2026-10-10）**：57 项全部完成。
> 验证证据：`.verify-logs/evidence-notion-style-ui/`（浅/深 × 6 态 + 768/414 窄屏共 16 张截图，
> 逐张人工核对通过，manifest.json 的 issues 为空）；
> `verify-all.sh` 连续两轮退出码 0（193/193）；typecheck / build 零错误；硬编码 grep 零命中。
> 第一轮 verify-all 曾红 upload/retrieval/compose 三项，根因为环境（mysql.exe 默认路径
> `D:/MySQL/...` 本机不存在、PATH 无带 PyYAML 的 python），与本次改动无关；
> 以 `MYSQL` 环境变量与 PATH 修正后两轮全绿（详见 docs/课程进度与后续路线.md §1.1）。
> 实施之后又做了一轮「面向用户」文案清理（去掉界面上的开发者向文本，未走 openspec）：
> 登录页副标题/脚注、工作台「开发者信息」块改 CSS 隐藏（`pre.raw` 保留 DOM 供 B8）、
> 知识检索页切片/字符区间/score 等术语改用户话术、命令面板与错误提示去技术化；
> 该轮后 verify-all 再跑一轮仍 193/193。
>
> 前置：`openspec/changes/` 下本 change 是唯一活动变更（已确认）。
> 全程**不改** `scripts/` 下任何断言；DOM 契约与文案按 design D9 冻结。
> 每步完成即勾选；未通过保持 `[ ]`（AGENTS 硬性纪律）。

## 1. 设计系统 Token 层（`frontend/src/styles.css`）

- [x] 1.1 `:root` 增补中性色阶：`--surface-3`、`--text-faint`、`--border-strong`；原 16 个 Token 按 design D2 定新值（`--brand: #94070a` 与 `--brand-fg` 原值保留）
- [x] 1.2 增补排版 Token：`--fs-12/13/14/15/17/20/24` 与对应行高（正文 15px / 1.7）
- [x] 1.3 增补间距 Token `--sp-1…--sp-12`（4 的倍数）、`--gutter`、`--w-read: 880px`、`--w-wide: 1040px`、`--w-login: 360px`
- [x] 1.4 增补形与动效 Token：`--r-sm:4 --r-md:6 --r-lg:10`、`--ctl-h:36 --ctl-h-sm:32`、`--shadow-float`、`--t-fast:120ms --t-base:160ms`
- [x] 1.5 `--shadow` 语义收敛为浮层专用（改名 `--shadow-float` 并同步全部引用）；内容块一律无阴影
- [x] 1.6 `@media (prefers-color-scheme: dark)` 覆盖块补齐全部新 Token（**`--bg` 保持 `#16161a`，见 D3**）
- [x] 1.7 全局补 `:focus-visible { outline: 2px solid var(--brand); outline-offset: 2px }` 与 `prefers-reduced-motion` 关闭动画（D7 / D8）

## 2. 基础元素与排版

- [x] 2.1 `body`：字体栈不变，字号/行高走 Token，中文行高放宽
- [x] 2.2 标题层级 `h1 / h2 / h3` 与 `.page-lead` 走 `--fs-*` + 字重，不再裸写数值
- [x] 2.3 `.muted` / `.footnote` / 元数据统一到 `--text-muted` / `--text-faint` 两档
- [x] 2.4 `code` / `kbd` / `pre` 等宽字体与尺寸统一（`.btn-palette kbd`、`.palette-hint`、`pre.raw`、`pre.raw` 保留在 DOM）
- [x] 2.5 数字类文本（大小、时间、计数、score）加 `font-variant-numeric: tabular-nums`
- [x] 2.6 `.alert` / `.alert-danger` / `.alert-notice` / `.alert-warn` 四态：淡底 + hairline + 左侧 3px 语义条，圆角降到 6px

## 3. 共享控件（五态齐全）

- [x] 3.1 `.btn-primary`：品牌底、高 36、圆角 4、去渐变/重阴影；hover / active / disabled / loading 四态
- [x] 3.2 `.btn-secondary`：中性底 + hairline 边框；四态
- [x] 3.3 `.btn-ghost`：透明底、hover 才出 `--surface-2`；disabled 用 opacity 且不失去边框辨识度
- [x] 3.4 `.input` / `.field input` / `.kn-select`：高度 36 统一、hairline 边框、focus 走品牌焦点环、disabled 态
- [x] 3.5 `.segmented` / `.seg-btn`：激活态改为 `--surface-3` 底 + 字重 600 + 底部 2px 品牌条（**不再整块红底**，D2）
- [x] 3.6 `.badge` / `.badge-teacher` / `.badge-student`：淡底 + hairline + 12px，语义色沿用 `--ok-*` / 中性

## 4. 头部与导航（`App.tsx`）

- [x] 4.1 `.app-header` 压到 56px，底部 hairline `--border`，去阴影，内部间距走 Token
- [x] 4.2 `.brand-mark` 竖条变细（3px / 18px）、`.brand-name` 15px/600
- [x] 4.3 `.nav-tab`：默认透明，hover `--surface-2`，`.active` 用 `--surface-2` + 字重 600（不用色块），圆角 6
- [x] 4.4 `.btn-palette` 与 `.who` / `.who .name` / `.who .muted` 对齐基线；`.who` 用 `--text-faint` 分隔，不加底框

## 5. 登录页（`LoginPage.tsx`）

- [x] 5.1 `.center-screen` / `.card`：宽 360、圆角 8、阴影降到 hairline + 极轻浮层阴影，内边距走 `--sp-*`
- [x] 5.2 `.brand` + `h1`（24px/600）+ `.subtitle`（`--text-muted` 13px）三段留白分层
- [x] 5.3 表单字段间距统一；`.field label` 13px `--text-muted`；输入框按 3.4
- [x] 5.4 提交按钮撑满卡片宽度；`.footnote` 12px `--text-faint`
- [x] 5.5 校验：`.card h1` 文本、`.alert-danger` 文案、`#username` / `#password` / `button[type="submit"]` 全部原样（D9）

## 6. 工作台首页（`HomePage.tsx`）

- [x] 6.1 `.page` 容器：左右 `--gutter`、最大宽 `--w-read`、`margin-inline: auto`；`.page h2` 20px/600 + `.page-lead` `--text-muted`
- [x] 6.2 `.home-hero` 去卡片化（不套白盒），靠字号层级与下方 `--sp-8` 留白分区
- [x] 6.3 `.home-actions` 按钮组间距 `--sp-3`，主次按钮按 3.1 / 3.2
- [x] 6.4 `.panel-head` / `.panel-body` / `.kv`：定义列表改两列网格（`dt` 13px `--text-muted` 固定宽、`dd` 14px），面板去阴影走 hairline
- [x] 6.5 `.dev-notes` 折叠块：summary 13px、hover 轻微；`pre.raw` 等宽 12.5px + `--surface-2` 底（**必须在 DOM，D9**）
- [x] 6.6 校验：`#recheck-session` 存在可点；学生 `.kv dd` 的「**只能查看与下载**」原文未改

## 7. 材料页（`MaterialsPage.tsx`）

- [x] 7.1 页面容器走 `--w-wide`；标题与 lead 按 6.1
- [x] 7.2 工具条单行：`.search-bar`（搜索框 `--flex` + 按钮）+ `.segmented` + 上传按钮，窄屏换行不溢出
- [x] 7.3 `.material-item`：行高 44、hover 只变 `--surface-2`（不位移）、hairline 分隔线替代卡片边框
- [x] 7.4 `.material-title` 14.5px/500、`.material-meta` 12.5px `--text-faint`；标题 hover 只变色不加下划线
- [x] 7.5 `.material-grid` 改 `repeat(auto-fill, minmax(220px, 1fr))`；`.material-card` 去阴影、hairline + 圆角 6、hover 边框加深
- [x] 7.6 `.detail-box`：左侧 3px 品牌竖条（Notion callout 感）+ `--surface-2` 底；`.detail-head` 元数据 12.5px
- [x] 7.7 `.markdown-body`：行宽约束 `--w-read`、标题层级、代码块/引用/表格 hairline 化；`.markdown-body a` 用 `--brand`
- [x] 7.8 `.placeholder` 空态：`--text-muted` 13px + 居中留白，不加边框

## 8. 知识检索页（`KnowledgePage.tsx`）

- [x] 8.1 `kn-*` 类族全部改走新 Token（颜色/圆角/间距），**结构与类名不动**
- [x] 8.2 `.kn-hit` 去卡片阴影改 hairline + hover `--surface-2`；`.kn-score` / `.kn-scorebar` 细化为 4px 条
- [x] 8.3 `.kn-answer` / `.kn-cite` / `.kn-mark` 沿用 `--highlight` 两套值，按新圆角与间距对齐
- [x] 8.4 skeleton / thinking 两个动画接入 `prefers-reduced-motion` 关闭（D8）

## 9. 浮层三件套

- [x] 9.1 `.palette-backdrop` / `.palette`：宽 560、圆角 10、`--shadow-float`；`.palette-input` 36px 无边框（底部 hairline）
- [x] 9.2 `.palette-item`：高 36、`.active` 用 `--surface-2` + 左侧 2px 品牌指示条；`.palette-hint` 12px `--text-faint`
- [x] 9.3 `.dialog` / `.dialog-head` / `.dialog-body` / `.dialog-foot`：标题 16px/600、分区留白、圆角 10
- [x] 9.4 `.dropzone`：虚线改 hairline 细虚线、`.dragging` 用品牌边框；`.progress` 细到 4px
- [x] 9.5 `.toast-host` / `.toast`：圆角 6、hairline + 左侧 3px 状态条；`.toast-close` hover 态补齐

## 10. 响应式

- [x] 10.1 ≥1024px：`--w-read` / `--w-wide` 居中生效，无横向溢出
- [x] 10.2 768px：header 允许换行/横向滚动、工具条换行、网格降到 2 列
- [x] 10.3 414px：`.page` 留白降到 16px、网格 1 列、命令面板宽度自适应 `calc(100vw - 32px)`
- [x] 10.4 判定：三个视口下 `document.documentElement.scrollWidth <= clientWidth`

## 11. 深色模式逐页核对（B20 相关）

- [x] 11.1 CDP `Emulation.setEmulatedMedia` 切 `prefers-color-scheme: dark`，六态各截一张：登录 / 工作台 / 材料列表 / 材料详情正文 / 知识检索命中 / 命令面板
- [x] 11.2 浅色同六态再截一组，存 `.verify-logs/evidence-notion-style-ui/`，写 `manifest.json`
- [x] 11.3 逐张人工核对：文字对比度、边框可见性、高亮可见性、浮层层级
- [x] 11.4 确认 `--bg` 深色值仍为 `#16161a`（B20 依赖）

## 12. 回归闸门（判据：退出码）

- [x] 12.1 `cd frontend && npm run typecheck` 通过
- [x] 12.2 `cd frontend && npm run build` 零错误
- [x] 12.3 `grep -rn "#[0-9a-fA-F]\{3,6\}\|rgb(\|hsl(" frontend/src --include=*.tsx` → 零命中
- [x] 12.4 `styles.css` 正文区（两个 Token 定义区之外）颜色字面量 → 零命中
- [x] 12.5 `bash scripts/verify-all.sh` 第一轮退出码 0，且各脚本断言数与基线一致（auth 21 / isolation 25 / upload 40 / retrieval 21 / health 19 / compose 17 / browser-verify 19 / e2e 31）
- [x] 12.6 `bash scripts/verify-all.sh` 第二轮退出码 0（连续两轮）
- [x] 12.7 `bash scripts/check-docs-freshness.sh` 不报新的过期项；过期则更新 `docs/` 对应内容
- [x] 12.8 `docs/课程进度与后续路线.md` 回写本次改造状态（唯一真相源，不能只在对话里汇报）

## 13. 收尾

- [x] 13.1 关掉本次启动的后台服务（按端口找 pid），清理孤儿 `conhost.exe --headless --server`
- [x] 13.2 不执行破坏性 Git 操作；不覆盖未提交的工作（改前 `git status` 确认）
- [x] 13.3 proposal「验收口径」逐条自查；未完成项与未验证项写进汇报
- [x] 13.4 归档本变更（移入 `openspec/changes/archive/`，更新 `AGENTS.md` 状态行）

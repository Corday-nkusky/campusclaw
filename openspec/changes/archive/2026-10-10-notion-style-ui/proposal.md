# Proposal: notion-style-ui（全站视觉改造 · Notion 风）

## 为什么改

迭代 1、2 的界面是「能用」级别：迭代 1 的登录页/材料页是一套自洽的卡片样式，迭代 2 的知识页
在 `polish-knowledge-ui` 里补齐了深色模式三处真缺陷并入了 `kn-` 类族，但两者是**先后拼起来的两套手感**：

- 迭代 1 的 `.panel` 一律「1px 边框 + 大阴影 + 14px 圆角」，知识页则是另一套 hairline 卡片 ——
  同一个产品里两种卡片语言。
- 阴影被用于表达「这是一块内容」，浮层（命令面板 / 弹窗 / Toast）反而没有更高的层级，
  页面看着「重」而层级是平的。
- 字号靠各处 `font-size: 12px / 13.5px / 14.5px` 现调，没有层级；标题、正文、元数据的区分
  主要靠颜色而不是字号与字重。
- 品牌红 `#94070a` 出现得过满（激活态分段控件是整块红底），中性色层级没建立起来。
- 内容宽度没有约束：宽屏下材料列表和 Markdown 正文被拉到整屏宽度，长文本阅读体验差。

用户本次的指示只有一句：**做成 Notion 风，干净清爽**；并明确授权「可以改以前的前端风格规定，
好看就行」。所以本次不守旧样式，但**守脚本契约**（见「不能碰的」一节）——那不是风格问题，
改了 `verify-all.sh` 就会红。

## 改什么

纯前端呈现层，落点按顺序：设计系统的 Token 层 → 基础元素与排版 → 共享组件 → 四个页面 →
浮层三件套（命令面板 / Toast / 上传弹窗）→ 响应式 → 深色模式逐页核对。

1. **Token 层重写（浅/深两套）**：在现有 `:root` + `prefers-color-scheme` 机制上重定
   中性色阶、间距、字号、圆角、阴影、焦点环；`--brand: #94070a` 保留。
2. **排版体系**：建立 13 / 14 / 15 / 17 / 20 / 24 的字号层级与三档字重，标题靠字号字重分层，
   不再只靠颜色；正文行宽受限（阅读型 ≤880px，数据型 ≤1040px）。
3. **去卡片化**：`.panel` 从「边框 + 阴影 + 大圆角」改为「留白 + hairline 分隔」，阴影只留给
   浮层（`.palette` / `.dialog` / `.toast` / `.kn-modal`），层级才立得住。
4. **共享组件统一**：按钮三态（primary / secondary / ghost）、输入框、分段控件、徽标、
   `kbd`，全部补齐 hover / focus-visible / active / disabled / loading 五态与统一控件高度。
5. **四个页面**：登录页、工作台（首页）、材料页（列表 / 网格 / 详情 / Markdown 正文）、
   知识检索页（`kn-*` 类族对齐新 Token，结构不动）。
6. **头部与导航**：header 压到 56px、底部 hairline；nav-tab 激活态改为轻量背景 + 字重，
   不再用整块品牌色。
7. **响应式与可访问性**：≥1024px 与窄屏（含 768 / 414）无溢出；键盘焦点全程可见；
   `prefers-reduced-motion` 下关闭过渡与动画。
8. **硬编码清零**：`frontend/src` 下除 `styles.css` 的 Token 定义区外，不得出现
   `#xxx` / `rgb(` / `hsl(` 字面量（现状：`.tsx` 里已经没有，本次补查 `styles.css` 正文区）。

## 为什么这样改（关键取舍）

| 备选 | 否决理由 |
|---|---|
| 引入 Tailwind / UI 组件库 | 换样式方案等于换构建与写法，与「视觉升级不是业务重写」冲突；且会淹没真实改动 |
| 引入图标库做视觉统一 | 项目零图标依赖，内联 SVG 够用；多一个依赖换不来验收分 |
| 深色模式做成手动切换按钮 | R10.1 的「深浅色切换」由 `prefers-color-scheme` 驱动且已被 B20 断言，加开关是**行为变更**，需 spec delta，不混进视觉变更 |
| 把品牌红降饱和成「Notion 紫」 | 用户明确要求保留品牌识别；红色改为**点缀**（主按钮 / 链接 / 焦点环 / 选区高亮），不铺面 |

## 影响面

- 代码：`frontend/src/styles.css`（主体）、`frontend/src/App.tsx`（header/导航结构微调）、
  `frontend/src/pages/{LoginPage,HomePage,MaterialsPage,KnowledgePage}.tsx`（类名与结构微调）、
  `frontend/src/components/{CommandPalette,Toast,UploadDialog}.tsx`（类名微调）。
- 文档：`docs/课程进度与后续路线.md`（回写状态）、`docs/` 里引用旧截图的说明（freshness 脚本判定为准）。
- **不改**：`backend/`、API 字段、路由、状态流转、业务逻辑、依赖清单（`package.json`）。

### 不能碰的（脚本契约，不是风格规定）

`scripts/` 下三个浏览器脚本用下列选择器与文案做断言，本次**只改样式不改这些名字与文案**
（改任何一个 `verify-all.sh` 都会红）：

| 脚本 | 依赖 |
|---|---|
| `browser-verify.mjs` | `#username`、`#password`、`form`、`button[type="submit"]`、`.card h1`、`.alert-danger`、`.badge`、`.who .name`、`.who .muted`、`.page h2`、`pre.raw`、`#recheck-session`、`.material-list` / `.placeholder`、`.material-title`、`.app-nav`、`.app-nav .nav-tab` |
| `e2e-acceptance.mjs` | 同上，另加 `.kv dd` 中学生的「**只能查看与下载**」原文 |
| `capture-identity-evidence.mjs` | 同上（取证截图） |

此外 **B20 断言把深色底色的精确值写死为 `rgb(22, 22, 26)`**（= `#16161a`），
所以 `--bg` 的深色值本次不动；需要层次靠 `--surface` / `--surface-2` / `--border` 拉开（design D3）。

## 为什么没有 specs delta

本变更**不新增也不修改任何 Requirement / Scenario**：R10.1 列的八个体验项（北大红品牌、深浅色切换、
列表/网格视图、本班搜索、⌘/Ctrl+K、上传进度、toast、Markdown GFM）**行为与可达性原样保留**，
只是变好看；R9.x 的身份来源、401 处理也不动。四件套里 `specs/` 为空是刻意的，与
`archive/2026-10-08-polish-knowledge-ui/` 同例（`.openspec.yaml` 声明 `skip_specs: true`）。

若实施中发现必须改动某个体验项的**可达性**（例如为了让窄屏不溢出而隐藏某个入口），
那属于行为变更，停下来补 delta，不在视觉变更里夹带。

## 与活动变更的关系

创建时 `openspec/changes/` 下无其它活动变更（迭代 1 / 1.5 / 2 的四份均已归档），本 change 是唯一活动变更。

## 验收口径（判据是命令退出码与截图，不是主观感受）

1. `cd frontend && npm run typecheck` 通过；`npm run build` 零错误。
2. `bash scripts/verify-all.sh` **连续两轮退出码 0**，且各脚本断言数与基线完全一致
   （auth 21 / isolation 25 / upload 40 / retrieval 21 / health 19 / compose 17 / browser-verify 19 / e2e 31）。
3. `grep` 硬编码颜色：`.tsx` 为零；`styles.css` 仅 Token 定义区允许。
4. 深浅两套主题 × 六个界面态（登录、工作台、材料列表、材料详情/正文、知识检索命中、
   命令面板）截图留证，逐张人工核对。
5. 窄屏（768 / 414）无横向溢出。
6. `bash scripts/check-docs-freshness.sh` 不报新过期；`docs/课程进度与后续路线.md` 已回写。

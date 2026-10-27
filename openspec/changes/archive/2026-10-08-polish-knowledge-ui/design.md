# 设计：polish-knowledge-ui

## 现状与问题定位

`KnowledgePage.tsx`（迭代 2，tasks 9.1–9.3）为赶验收用内联样式直出，三处硬编码颜色在
深色模式下出错（详见 proposal）。`styles.css` 的 token 体系（--brand/--surface/--text/
--border/--shadow 等，浅深两套）是迭代 1 验证过的底座，本变更只消费、不推翻。

## Decisions

### D1：类名族与归属

新增类统一 `kn-` 前缀，全部放 styles.css 尾部新分区「知识检索页」，与既有分区风格一致
（分区注释 + 主题色只从变量取）。组件侧删光内联 `style` 属性，保留动态逻辑（无）。

### D2：只补一枚 token

新变量 `--highlight`（命中卡内查询词高亮、弹层 mark 共用）：
- 浅色：`#ffe58a`（沿用现有 mark 色，对比度已验证）配 `--text` 前景；
- 深色：`#5c4a12`（暗琥珀，白字 `--text` 下对比达标）。
其余一律复用既有变量。理由：颜色越多，两主题的核对面越大；一枚新 token 是上限。

### D3：score 分值条

命中卡右上角：`score 0.4963` 数字（tabular-nums）+ 4px 高分值条。
分值条只做**相对呈现**：`width: calc(score * 100%)`，不假装是绝对概率
（hybrid 的 RRF 分与 keyword/vector 分不同刻度，条形图只是排序辅助，不是计量仪表——
呈现层不做它会误导的换算）。

### D4：snippet 查询词高亮是客户端行为

`<mark>` 包裹命中片段里与查询词重叠的部分（不区分大小写的字面匹配，CJK 不分词）。
明确边界：这是**呈现层美化**，不参与召回/排序，后端 API 与 R3.3 偏移量语义零影响。
匹配不到（如纯语义召回）就不高亮，不硬凑。

### D5：三态呈现

- 加载：两枚 skeleton 命中卡（CSS 渐变动画，无依赖）；
- 空（`hits !== null && hits.length === 0`）：沿用 `.placeholder` 虚线框 + 引导文案
  （「换个说法试试 / 切换检索模式」），与材料页空态同款观感；
- 错误：`.alert-danger` 替换现在的 `<p style="color:crimson">`，迭代 1 的提示条体系复用。

### D6：回原文弹层

- `var(--fg)` → `var(--text)`（缺陷修复本体）；
- 头部加 `--brand` 竖条（呼应登录页 .brand-mark）+ 材料标题 + 切片区间徽标；
- mark 用 `--highlight`；支持 ESC 关闭（useEffect 监听，弹层打开时挂载）；
- 背景遮罩与命令面板 `.palette-backdrop` 同参数（rgb(12 12 16 / 32%)），观感统一。

### D7：验证脚本零改动

本变更不动 `scripts/` 下任何文件。约束即验收：改完跑 browser-verify 必须 19/19
（其选择器：#username/#password/.card h1/.alert-danger/.badge/.who/.page h2/pre.raw/
#recheck-session/.btn-ghost[1] —— 均不在知识页，重构互不影响；e2e-acceptance.mjs
实施时先核对一遍有无知识页选择器，有则以不破坏为前提调整类名落点）。

## 风险

- e2e-acceptance 若隐式依赖知识页 DOM 结构（低概率，初步核对无），实施时第一时间发现；
- `kn-` 样式与既有类的特异性冲突：新分区放文件尾部 + 不复用易撞名类，风险可控。

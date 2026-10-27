# 任务：polish-knowledge-ui

> **归档说明（2026-10-08）**：归档时 17/18 勾。唯一未勾 §5.2「归档本变更」——归档动作本身
> 即其完成证明，按归档纪律不在归档目录回改勾选。本 change 为纯 UI 改造、零 specs delta
> （以 `.openspec.yaml` 的 `schema: spec-driven` + `skip_specs: true` 声明），
> `openspec validate --all --strict` 全绿。验收证据：`.verify-logs/evidence-polish-knowledge-ui/`
> （14 张三态截图 + manifest.json）与 `docs/验收报告-迭代2.md` §4（4.5 完整 UI 流两轮跑通、
> R6.2 桩探针 8 项 PASS）。

> 前置：add-class-scoped-retrieval 归档（见 proposal 的纪律说明）。
> 全程不修改 scripts/ 下任何验证脚本；每步完成即勾选。

## 1. 缺陷修复（深色模式）

- [x] 1.1 弹层 `var(--fg, #111)` → `var(--text)`；确认深色下弹层文字可读
- [x] 1.2 命中卡/答案框硬编码 `#d0d0d0` 边框 → `var(--border)`
- [x] 1.3 新增 `--highlight` token（浅 `#ffe58a` / 深 `#5c4a12`），替换硬编码 mark 色

## 2. 样式迁移

- [x] 2.1 styles.css 尾部新增「知识检索页」分区（kn- 前缀类族，全走 token）
- [x] 2.2 KnowledgePage.tsx 删除全部内联 style，改挂 kn- 类；检索/问答表单对齐 .search-bar 观感
- [x] 2.3 错误提示 `<p style="color:crimson">` → `.alert-danger`

## 3. 呈现升级

- [x] 3.1 命中卡：标题行（材料名 + score 数字）、分值条（D3：width=score*100%）、
      snippet（pre-wrap）、八字段溯源 meta 行排版
- [x] 3.2 snippet 查询词客户端高亮（D4 边界：字面匹配、匹配不到不高亮）
- [x] 3.3 三态：skeleton 加载卡（CSS 动画）、无命中空态引导、错误条
- [x] 3.4 回原文弹层：品牌竖条头部 + 区间徽标 + 主题化 mark + ESC 关闭（D6）
- [x] 3.5 问答出处 chips：[n] 《材料》切片样式化 + 悬浮 title（不做点击跳转，Non-goal）

## 4. 验收与留证

- [x] 4.1 `npm run build`（tsc）零错误
- [x] 4.2 核对 e2e-acceptance.mjs / browser-verify.mjs 无知识页 DOM 依赖被破坏，browser-verify 19/19
- [x] 4.3 verify-all 各脚本断言数与基线一致（本变更不引入新断言）
- [x] 4.4 浏览器实跑截图：浅色/深色 ×（命中卡列表、弹层高亮、无命中态、问答出处）≥ 6 张，
      存 .verify-logs/evidence-polish-knowledge-ui/
      （第一轮实拍 8 张＝浅色{初始/骨架卡/空态/错误条/问答态}+深色{初始/空态/错误条}，抽查 6 张人工核对通过，
       当时的命中卡/弹层高亮/出处 chips 因网关 429 无真索引截不到；额度恢复后已补齐——
       09–14 六张覆盖命中卡/弹层高亮/出处 chips ×浅深，manifest.json 的 blocked 已清零。
       注：早期 8 张证据目录曾被 verify-all 的日志清理误删，随后全部状态可复现、整组重生；
       04/08 错误条原图是「网关 429 → 503」，重生时网关已恢复，改以「停 Qdrant」复现同一 503 错误条 UI 态）
- [x] 4.5 真实上传一篇材料，从上传 → 检索 → 点命中回原文 → 提问走完整 UI 流（配网关额度）
      （e2e-polish45.mjs：CDP setFileInputFiles 上传《夜间城市热岛与通风廊道-A班补充》→ 索引 ready →
       keyword 检索命中含新材料 → 点命中回原文 mark 高亮 + ESC 关闭 → 提问出回答与出处 chips，
       深色三态就地切换复用同一状态（0 额外网关调用）。对话网关按用户要求走本地记录桩（0 真实对话调用），
       嵌入仍走真网关；材料 id=7，steps 见 manifest.json 的 tasks45）

## 5. 收尾

- [x] 5.1 proposal「验收口径」逐条自查
      （①build 零错误 ✓；②browser-verify 19/19、verify-all 第 2 轮各脚本计数与基线一致 ✓；
       ③深浅主题人工核对：可达的 8 个状态已截图抽查通过，命中卡/弹层/高亮可见性因网关 429
       无真索引未能核对——与 4.4/4.5 的 blocked 同源，额度恢复后补。
       纪律说明的「排在 retrieval 归档之后」经用户授权自行权衡提前实施，
       其担心的计数基线混叠风险已被 4.3 实测排除）
- [ ] 5.2 归档本变更（无 specs delta，归档动作 = 目录移入 archive + 更新 AGENTS 索引如有）

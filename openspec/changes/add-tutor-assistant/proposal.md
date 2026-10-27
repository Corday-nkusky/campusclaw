# Proposal: add-tutor-assistant

## Why

第 4 课把「可追溯检索」做成了独立功能页（`POST /api/search` / `POST /api/ask`），
用户要自己判断检索什么、怎么把结果组织成回答。

第 5 课要解决的是**架构形态**问题：检索不该是用户面对的功能页，而应成为助手**行动链内部的一步**。
课件原文（`scope.html` §1）：

> 登录、角色校验、上传入库和本班检索沿用前两课。知识库已经能按班级检索并标出出处，
> 本课把它接到助手的行动里，不再单独做一个检索页。

一次提问内部依次经过四段（课件 `session.html` / `route.html` / `stream.html`）：
**提示词（助手是谁）→ 技能（这题怎么导）→ 本班知识库检索（依据是什么）→ SSE 流式作答（怎么送达到用户）**。

四段缺一不可，缺任一段都无法验收：

| 缺哪段 | 后果 |
|---|---|
| 提示词 | 助手没有角色，回答千篇一律 |
| 技能 | 只能直接给结论，无法「引导而不给答案」 |
| 检索 | 回答没有依据，违反课程「找不到不许编」的硬约束 |
| 流式 | 长时间等待，用户看不到进度，且引用只能在正文结束后才出现 |

**当前仓库的实际状态**：第 5 课的功能**已经实现**（`internal/prompt`、`internal/skills`、
`internal/agents`、`internal/conversation`、`internal/knowledge/chatstream.go`，
另有 `apitest/` 12 个用例与 `scripts/e2e-tutor.mjs` 16 项浏览器断言），
但**这些代码没有对应的规约**——`openspec/specs/` 下只有 `auth-upload` 与 `knowledge-retrieval`
两个 capability，`knowledge-retrieval` 的 R6.7 甚至明写「本课不实现流式输出」。

即：**代码先行、规约缺位**。这与本仓 AGENTS.md 的硬性纪律「规约先行；发现规约含糊或缺失，先改规约再改代码」
直接冲突，且期末验收要求「仅凭一条 Scenario 能否判定通过/不通过」——
当前第 5 课的功能**无法用规约判定**，只能靠看代码。

## What Changes

本 change 的性质与前几轮不同：**功能已实现，本轮补上它的规约**，并据此核对实现、补齐缺口。
具体包含：

- **新增 capability `tutor-assistant`**，覆盖第 5 课的五组能力：
  1. **R1 提示词**：教师维护一段助手角色提示词（≤2000 字），空值回退默认角色。
  2. **R2 技能文件**：中文五键格式的技能文件（名称/适用条件/工具/资料/启用），
     解析、长度与工具名校验，班内名称唯一。
  3. **R3 智能体与绑定**：智能体 = 名称 + 提示词 + 绑定的技能与材料；
     绑定是关联关系而非快照，**每次提问重读**，教师改绑定立即生效。
  4. **R4 会话与消息**：会话锁定「用哪个智能体」；消息落库；
     提问前取最近 6 条历史、每条截断 500 字。
  5. **R5 技能路由与流式作答**：路由至多选中一个技能；检索只在绑定材料内取前 4 条；
     SSE 依次下发 `meta` → `delta*` → `done`（失败时 `error`），`meta` 恒为 `ok`。
- **接口**：`GET/PUT /api/prompt`、`GET/POST /api/skills`、`PUT/DELETE /api/skills/{id}`、
  `GET/POST /api/agents`、`GET/PUT /api/agents/{id}`、
  `GET/POST /api/conversations`、`GET /api/conversations/{id}`、
  `POST /api/conversations/{id}/messages`（SSE）。
- **文档**：`GET /openapi.json` 与 `GET /swagger/`（Swagger UI）——由处理器注解同源生成。

## Non-goals

- **不改变第 3、4 课已交付的能力**（认证授权、上传入库、检索与引用标注的判定逻辑一律不动）。
- **不做重排序、跨班检索、跨会话共享上下文**。
- **不做多智能体协作、工具调用（MCP）**——MCP 是第 6 课的范围。
- **不引入新的第三方依赖**：数据库、检索与对话网关沿用第 4 课配置。
- **不改 `openspec/specs/` 主规约**——按纪律，主规约只由 Archive 更新；本轮只写 delta。
- **不重跑全量验收脚本作为本轮交付**：`verify-all.sh` 与 `apitest/` 的实跑由独立的验证任务执行，
  本轮以「delta 与实现逐条对齐」为准。

## 关键取舍

| 备选 | 否决理由 |
|---|---|
| 沿用 `knowledge-retrieval` capability，把第 5 课当它的延伸 | 检索与助手是**两层**：检索是能力，助手是编排。把 `meta`/`delta`/技能路由塞进检索规约会让 capability 名不副实，且 R6.7「本课不实现流式」需要被推翻——不如另立 capability |
| 只写 design 文档，不写 spec delta | 验收标准是「仅凭一条 Scenario 能否判定通过/不通过」。没有 Scenario 的设计文档不是规约 |
| 用 `add-tutor-assistant` 之外的命名 | 课程未公布第 5 课的 change 名。**本轮自拟**，与既有 `add-class-scoped-retrieval` / `add-token-auth` 保持 `add-<能力>` 风格；归档时若与课程口径不一致，改名成本极低（目录级改名） |

## 影响面（capability：`tutor-assistant`）

- **spec delta**：**ADDED** R1–R5（新建 capability，主规约无同名 baseline，故全为 ADDED）。
- **代码**：**已存在**，本轮只做「delta 逐条对齐 + 补缺口」，不重写。
- **验收脚本**：`scripts/e2e-tutor.mjs`（16 项）、`apitest/`（12 用例）已存在，
  本轮核对它们是否覆盖 delta 的每一条 Scenario；覆盖不到的补用例。

# AGENTS.md · CampusClaw（第 7 课：五类工程规范已落成规则 · 当前有 3 个活动 change）

本文件是 **harness 的一部分，不是参考意见**。任何在本仓工作的智能体都必须先读本文件，
并自行确认 `openspec/changes/` 下当前是否还有活动 change。
**当前状态（2026-10-27）：3 个活动 change** —— `add-tutor-assistant`（第 5 课解题助手，
capability `tutor-assistant`）、`add-refresh-token`（双令牌，capability `auth-upload`）、
`add-mcp-tool-bridge`（第 6 课外部工具与 MCP 接入，capability `mcp-tool-bridge`）。
已归档：
迭代 1 `archive/2026-09-23-add-auth-rbac-class-knowledge/`、迭代 1.5 `archive/2026-10-08-add-token-auth/`
（token 登录改造，主 spec 的 R1/R2/R9 已是 token 版）、迭代 2 第 4 课
`archive/2026-10-08-add-class-scoped-retrieval/`（capability `knowledge-retrieval`）、
`archive/2026-10-08-polish-knowledge-ui/` 与 `archive/2026-10-10-notion-style-ui/`
（全站 Notion 风视觉改造 + 界面文案用户化，纯前端呈现层，零 specs delta）。
归档目录是历史；**长期真相源是 `openspec/specs/`**。

> **第 5 课的规约是后补的**（2026-10-21）：功能此前已实现并跑通，但 `openspec/` 下一直**没有**
> 对应的 change——即「代码先行、规约缺位」，导致该课的功能**无法用 Scenario 验收**。
> 现补 `add-tutor-assistant`（ADDED R1–R7，44 条 Scenario）与 `add-refresh-token`。
> 两条都**尚未归档**——归档会回写 `openspec/specs/`，需独立确认后执行。

> 前端两条长期硬约束（源自 notion-style-ui design.md D3 / D9，归档后依然有效）：
> **深色 `--bg` 必须是 `#16161a`**（`browser-verify.mjs` B20 把 `rgb(22, 22, 26)` 写死在断言里）；
> **脚本依赖的选择器与「只能查看与下载」文案一个字符都不能改**
> （含 `pre.raw`：界面上用 CSS 隐藏，但 DOM 必须在，B8 要解析它）。

## 开工前必须做的事

1. 长期真相源是 `openspec/specs/`：`auth-upload/spec.md`（登录 / 角色 / 班级隔离 / 上传，token 版）、
   `knowledge-retrieval/spec.md`（检索 / 问答 / 引用标注 / 失败语义）。
   尚未归档的两个 capability，真相源暂在各自 change 内：
   `openspec/changes/add-tutor-assistant/specs/tutor-assistant/spec.md`（第 5 课）与
   `openspec/changes/add-mcp-tool-bridge/specs/mcp-tool-bridge/spec.md`（第 6 课）。先读与新任务相关的那份。
2. 新功能先走 OpenSpec 流程：新建 change 四件套（proposal / design / specs delta / tasks），再实现。
   ⚠️ **不要重复第 5 课的错**：功能先做完、规约后补，会让该课的功能**无法用 Scenario 验收**。
3. **先复述本次范围与 Non-goals**，复述有误当场纠偏，不要带着错误理解写代码。
4. 确认：本次涉及的 change 四件套齐全且要做的 task 尚未勾选。
5. 读 `docs/standards/` 五份工程规范（见下节）。**它们是硬约束，不是建议。**

## 工程规范（第 7 课 · 五类，全部落在本文件与 `docs/standards/`）

课件口径：规范只有两种落法——常驻约束写成规则（规则文件或根目录 `AGENTS.md`），
按场景执行的流程写成技能。"没落进这两处的规范，等于没有规范。"

| # | 规范 | 文件 | 三条最容易被违反的硬点 |
|---|---|---|---|
| 01 | 目录与分层 | `docs/standards/01-目录与分层规范.md` | 依赖单向（cmd → 领域 → 基础设施，禁反向边）；外部依赖只在最外侧；不新建 `util/` `common/` 这类无主语包 |
| 02 | 前后端开发 | `docs/standards/02-前后端开发规范.md` | 前端请求只从 `src/api.ts` 出；三态（加载/错误/空）齐全且错误语义不可改写；后端错误体固定 `{"error","detail"}`，`error` 不含动态内容 |
| 03 | 测试 | `docs/standards/03-测试规范.md` | 每能力「正常/失败/边界」三类各一；断言不只查状态码；改用例数必须同步改 `verify-all.sh` 的预期值 |
| 04 | 安全 | `docs/standards/04-安全规范.md` | 默认拒绝 + fetch-then-check；跨班与不存在同为 404；密钥只从环境变量读且不进日志 |
| 05 | 日志与观测 | `docs/standards/05-日志与观测规范.md` | 一行一条 JSON（`internal/obslog`），带 `request_id`；**全部输出走 `obslog`，不得使用 `log.Printf`** |

验证规范真的生效（第 7 课要求"改一次代码验证它确实生效"）：
证据在 `docs/lesson-07/验证记录.md` —— 日志 JSON 化的实际报文、分层依赖的实际输出、
前端类型检查与后端测试的退出码。

## 硬性纪律

| 纪律 | 说明 |
|---|---|
| **只动 `changes/`** | `openspec/specs/` 是长期真相源，**只由 Archive 更新**。Apply 期间要改规约，只能改 change 内的 delta |
| **规约先行** | 发现规约含糊或缺失，**先改规约再改代码**。不允许用代码迁就含糊描述 |
| **驳回要引原文** | 驳回生成物必须引用 Scenario 原文，不靠追加对话扩大范围 |
| **不许一次生成全仓** | 一个 task 一轮：实现 → 审查 diff → 运行 verify → 提交 |
| **失败项不带入下一项** | 未通过的 task 保持 `[ ]` |
| **判定不由模型下** | 「是否合格」由 Scenario 与运行结果判定，不由模型自己的结论判定 |
| **不许复制他人仓库** | 也不许沿用第 2 课 Propose 的旧对话继续写代码 |

## 安全红线（写代码时不得违反）

1. **访问控制必须在服务端强制执行。** 前端隐藏入口只降低可发现性，**不构成访问控制**。
2. **租户（班级）只取自服务端会话。** 请求里的 query / Header / 表单中的 `class_id` 一律忽略（D2）。
3. **跨班访问与「资源不存在」返回完全同形的 404**，不含标题、正文、磁盘路径（D3）。
4. **按 ID 取行用 fetch-then-check**，不在 SQL 里提前过滤班级（D4）。
5. **密钥只从环境变量读**，缺失即启动失败，不得内置默认值（D7）。
6. **磁盘存储名由服务端生成**，客户端文件名不参与路径构造（D8）。
7. **会话凭证 = 服务端签发的 HS256 JWT（`Authorization: Bearer`）**：token 里只放 `user_id` 身份标识，**`role` 与 `class_id` 每次从库读取，不采信 token 或请求中的任何其他值**（迭代 1.5 D2 后的现行版，取代旧「不签发 JWT」红线）；客户端只保存 token 本身（localStorage `cc_token`）。
8. **`/health` 不查库**（D9）。
9. **Qdrant 与两类模型网关不对客户端暴露**：只在 compose 内部网络，端口不映射宿主机、不经 Nginx 反代；密钥只从环境变量读。
10. **检索与问答的班级条件只在服务端拼**：关键字路与向量路都要带，回表取正文时再核对一次（迭代 2 D6）。
11. **无命中切片时不得调用对话网关**：直接返回固定文案「资料中未找到相关内容」，`citations` 为空（迭代 2 D8）。
    ⚠️ **此条只适用于第 4 课的独立问答接口 `POST /api/ask`**。第 5 课的会话链路
    `POST /api/conversations/{id}/messages` 口径**不同**：`meta.status` 恒为 `ok`，
    检索落空**仍调模型作答**（`citations` 为空、正文不含 `[n]` 编号）。
    两者不可混用——这是第 5 课 design D8 的明确决议。
12. **检索到的资料不进系统消息**（第 5 课 D9）：材料正文是教师上传的外部输入，
    放进系统消息等于给不可信文字发指令的权限。系统消息只放**角色提示词**、**技能说明**与边界句；
    资料一律作为数据进用户消息一侧。
13. **技能与材料的绑定每次提问重读**（第 5 课 D4）：绑定存为关联表而非快照，
    教师改绑定或停用技能后**立即生效**，不必新建会话。
    反过来，**智能体停用不影响已有会话**（会话锁定的是「用哪个智能体」）。

## 明确不做（不得自行补回）

**迭代 1 的边界**：~~RAG / 向量检索 / 问答~~（✅ **第 4 课已交付并归档**，现行规约 `openspec/specs/knowledge-retrieval/spec.md`）、
~~对话助手~~（✅ **第 5 课已实现**，规约 `add-tutor-assistant`，capability `tutor-assistant`，**待归档**）、
作业布置与批改、
**平台超级管理员**、OAuth/SSO、注册改密、多校多租户、PDF/Word/图片解析、K8s/CI/HTTPS/多副本。

**迭代 2（第 4 课）新增的明确不做**：重排序（cross-encoder）、编排框架（LangChain / LlamaIndex / Dify）、流式输出、多轮会话持久化。
详见归档 change 的 proposal Non-goals（`archive/2026-10-08-add-class-scoped-retrieval/`）——**以 `openspec/specs/` 为准，本表只是索引**。

> ⚠️ 其中「流式输出、多轮会话持久化」两项**已被第 5 课推翻**——第 5 课正是做流式（SSE）与会话持久化。
> 那句是第 4 课当时的边界，不是全仓的永久边界；第 5 课的边界见 `add-tutor-assistant` 的 Non-goals
> （不做 MCP、不做多智能体协作、不做跨会话共享上下文）。
> 第 4 课其余的「不做」仍然有效。

> **平台超级管理员**是最容易被越界实现的一条：即便界面上看不见，一条可跨班的查询分支**也已经绕过了隔离**。

## 回答方式要求

被问及实现状态时，逐项给出证据：**文件与行号、命令与输出**。找不到就明说「未实现」，**不许推测**。

## 事实分级

本仓文档中的陈述分三级，不要混用：**【教材原文】**（课件写明）/ **【我方定义】**（本仓补充）/ **【未验证】**（未实测）。

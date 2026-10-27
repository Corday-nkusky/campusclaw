# spec delta: tutor-assistant

## ADDED Requirements

### Requirement: R1 助手提示词

系统 SHALL 为每个班级维护一段助手角色提示词，由教师读写。提示词为空时系统 SHALL 回退到默认角色设定，
**不因此拒绝请求**。提示词 SHALL 按班级隔离：只取自服务端会话的 `class_id`，
请求体中的任何 `class_id` 一律忽略。

#### Scenario: R1.1 教师读写本班提示词

- **WHEN** 教师携带有效令牌 `GET /api/prompt`
- **THEN** 返回 200，响应体含该班当前提示词正文
- **AND** `class_prompts` 表中该班对应行存在（未设置过时为空串）
- **WHEN** 教师携带有效令牌 `PUT /api/prompt`，正文为一段不超过 2000 字的提示词
- **THEN** 返回 200，且 `class_prompts` 中该班行被更新为该正文

#### Scenario: R1.2 学生不可写提示词

- **WHEN** 学生令牌调用 `PUT /api/prompt`
- **THEN** 返回 403，响应体不含业务数据
- **AND** `class_prompts` 表无任何行被修改

#### Scenario: R1.3 提示词按班级隔离

- **WHEN** A 班会话读取或写入提示词
- **THEN** 读写的是 A 班那一行
- **AND** 请求体携带 `"class_id": <B 班 ID>` 时被忽略，结果仍落在 A 班
- **AND** B 班的提示词内容**不受影响**

#### Scenario: R1.4 提示词为空时回退默认角色

- **WHEN** 某班提示词为空字符串，且该班存在已启用智能体，学生用该智能体新建会话并提问
- **THEN** 请求**不被拒绝**（不返回 400）
- **AND** 组装出的系统消息中含**默认角色设定**（而非空串）

#### Scenario: R1.5 超长提示词被拒

- **WHEN** 教师 `PUT /api/prompt` 的正文超过 2000 字
- **THEN** 返回 400，响应体给出可读错误
- **AND** `class_prompts` 中该班行**保持原值**（不写入被截断的内容）

### Requirement: R2 技能文件与技能增删改查

系统 SHALL 以**技能文件原文**为唯一录入形式：空行以上为中文键（`名称` / `适用条件` / `工具` / `资料` / `启用`），
空行以下为技能说明正文。系统 SHALL 校验字段完整性、长度上限与工具名，
并 SHALL 以班内名称唯一约束技能。写操作 SHALL 仅限教师。

#### Scenario: R2.1 合法技能文件被接受

- **WHEN** 教师 `POST /api/skills`，`source` 为合法技能文件（空行以上五键齐全、空行以下有正文）
- **THEN** 返回 201，响应体含解析出的 `name` / `trigger` / `subject` / `tools` / `enabled` / `description`
- **AND** `skills` 表新增一行，其 `class_id` 等于会话所在班级
- **AND** 该行 `source` 保存**技能文件原文**

#### Scenario: R2.2 缺字段被拒

- **WHEN** 教师提交的技能文件缺少 `名称`、`适用条件` 或 `启用` 任一项
- **THEN** 返回 400，响应体给出**指明缺失项**的可读错误
- **AND** `skills` 表无新增行

#### Scenario: R2.3 重复字段被拒

- **WHEN** 教师提交的技能文件中同一键出现两次
- **THEN** 返回 400
- **AND** `skills` 表无新增行

#### Scenario: R2.4 非法工具名被拒

- **WHEN** 教师提交的技能文件中 `工具` 写的不是 `knowledge_search`
- **THEN** 返回 400，错误信息指明工具名非法
- **AND** `skills` 表无新增行

#### Scenario: R2.5 启用值只接受是或否

- **WHEN** 教师提交的技能文件中 `启用` 写的不是「是」或「否」
- **THEN** 返回 400
- **AND** `skills` 表无新增行

#### Scenario: R2.6 空行以下缺少正文被拒

- **WHEN** 教师提交的技能文件**没有分隔空行**，或空行以下正文为空
- **THEN** 返回 400，错误信息指明需要「空行 + 正文」
- **AND** `skills` 表无新增行

#### Scenario: R2.7 超长技能文件被拒

- **WHEN** 教师提交的技能文件整体超过 8000 字
- **THEN** 返回 400
- **AND** `skills` 表无新增行

#### Scenario: R2.8 班内同名提交返回 400

- **WHEN** 教师提交的技能名称与**本班已有技能**同名
- **THEN** 返回 **400**（不是 409），响应体给出可读错误
- **AND** `skills` 表**不新增行**，已有那条**不被覆盖**

#### Scenario: R2.9 他班技能按编号直取返回 404

- **WHEN** A 班会话 `GET /api/skills`（列表）
- **THEN** 返回 200，列表中**只有本班**技能，不含他班技能
- **WHEN** 教师对**他班**技能编号调用 `PUT /api/skills/{id}` 或 `DELETE /api/skills/{id}`
- **THEN** 返回 404，且响应体与「该编号根本不存在」时**完全一致**
- **AND** 他班那一行**不被修改、不被删除**

#### Scenario: R2.10 技能读写权限分离

- **WHEN** 学生令牌 `GET /api/skills`
- **THEN** 返回 200，可读本班技能
- **WHEN** 学生令牌调用 `POST /api/skills` / `PUT /api/skills/{id}` / `DELETE /api/skills/{id}`
- **THEN** 三者均返回 403
- **AND** `skills` 表无任何行被修改

#### Scenario: R2.11 技能同内容重复提交是幂等覆盖而非新增

- **WHEN** 教师对同一编号 `PUT /api/skills/{id}` 两次，提交**相同**的技能文件
- **THEN** 两次均返回 200
- **AND** `skills` 表中该编号**只有一行**（不新增行）

#### Scenario: R2.12 改后与另一条技能同名被拒

- **WHEN** 教师把某技能改名，改成**本班另一条技能**的名称后提交
- **THEN** 返回 400
- **AND** 原行的名称**保持不变**

### Requirement: R3 智能体与绑定关系

系统 SHALL 支持「智能体」：一个智能体 = 名称（≤64 字）+ 角色提示词（≤2000 字）+ 一组绑定技能 + 一组绑定材料。
绑定 SHALL 存为**关联关系**而非快照，**每次提问重新读取**——教师改绑定或停用技能后**立即生效**，
不必新建会话。智能体 SHALL 按班级隔离。

#### Scenario: R3.1 教师创建智能体

- **WHEN** 教师 `POST /api/agents`，提交名称、提示词与绑定项
- **THEN** 返回 201，响应体含 `id` / `name` / `prompt` / `skills` / `materials` / `enabled`
- **AND** `agents` 表新增一行，`class_id` 等于会话所在班级
- **AND** 绑定项分别落在 `agent_skills` 与 `agent_materials` 两张关联表

#### Scenario: R3.2 学生不可写智能体

- **WHEN** 学生令牌调用 `POST /api/agents` / `PUT /api/agents/{id}`
- **THEN** 返回 403
- **AND** `agents` 表无任何行被修改

#### Scenario: R3.3 他班智能体不可见不可取

- **WHEN** A 班会话 `GET /api/agents`
- **THEN** 列表中只有本班智能体
- **WHEN** 对**他班**智能体编号调用 `GET /api/agents/{id}`
- **THEN** 返回 404，且与「编号不存在」的响应**完全一致**

#### Scenario: R3.4 绑定是关系不是快照

- **WHEN** 智能体已绑定技能 S 与材料 M，随后教师把 S 停用、把 M 从绑定中移除
- **AND** 学生用**同一个已有会话**再提问
- **THEN** 该次提问**不进入**技能 S（技能不再进候选集）
- **AND** 检索范围**不再包含**材料 M
- **AND** 该变化**无需新建会话**即已生效

#### Scenario: R3.5 智能体停用后已有会话仍可提问

- **WHEN** 智能体被停用（`enabled = 0`），而某会话此前已锁定该智能体
- **AND** 学生对该会话继续提问
- **THEN** 请求**不被拒绝**，仍按该智能体的提示词与绑定作答

#### Scenario: R3.6 教师删除未被会话引用的智能体

- **WHEN** 教师 `DELETE /api/agents/{id}`，该智能体属于本班且没有任何会话引用它
- **THEN** 返回 204
- **AND** `agents` 表删除该行，`agent_skills` / `agent_materials` 关联随级联一并移除

#### Scenario: R3.7 已被会话引用的智能体不可删除

- **WHEN** 教师 `DELETE /api/agents/{id}`，存在至少一个会话引用该智能体
- **THEN** 返回 409，响应体说明原因并指向「停用」这一替代做法
- **AND** `agents` 表保持不变，历史会话仍可回看

#### Scenario: R3.8 删除智能体的越权与不存在同形

- **WHEN** 学生令牌调用 `DELETE /api/agents/{id}`
- **THEN** 返回 403
- **WHEN** 教师对**他班**或不存在的编号调用 `DELETE /api/agents/{id}`
- **THEN** 返回 404，且与「编号不存在」的响应**完全一致**
- **AND** 该智能体**不出现在**新建会话的可选列表中

### Requirement: R4 会话与消息

系统 SHALL 提供会话的列表、新建、按编号读取，以及在会话中发消息。
会话 SHALL 锁定**用哪个智能体**；消息 SHALL 落库。提问时系统 SHALL 取该会话**最近 6 条**消息作为上下文，
每条**截断 500 字**。会话 SHALL 按班级与归属隔离。

#### Scenario: R4.1 新建会话锁定智能体

- **WHEN** 学生携带有效令牌 `POST /api/conversations`，指定本班已启用的智能体
- **THEN** 返回 201，响应体含 `id` 与所选智能体信息
- **AND** `conversations` 表新增一行，其 `user_id` / `class_id` / `agent_id` 分别等于会话用户、所在班级、所选智能体

#### Scenario: R4.2 新建会话时智能体不存在或未启用返回 404

- **WHEN** 学生 `POST /api/conversations` 指定的智能体**不存在**、**不属于本班**或**已停用**
- **THEN** 返回 404
- **AND** 响应体与「该智能体编号不存在」时**完全一致**
- **AND** `conversations` 表无新增行

#### Scenario: R4.3 跨班会话按编号直取返回 404

- **WHEN** A 班会话 `GET /api/conversations/{id}`，该编号属于 B 班
- **THEN** 返回 404，响应体与「编号不存在」时**完全一致**
- **AND** 响应体**不含**会话标题、智能体名等任何可区分信息

#### Scenario: R4.4 会话列表只含自己的

- **WHEN** 学生 `GET /api/conversations`
- **THEN** 返回 200，列表中只有**本人**的会话
- **AND** 同班其他同学的会话不出现

#### Scenario: R4.5 提问长度校验

- **WHEN** 提问正文少于 2 个字符
- **THEN** 返回 400，错误信息可读
- **WHEN** 提问正文超过 500 字
- **THEN** 返回 400
- **AND** 两种情况**均不落库**（`messages` 表无新增行）

#### Scenario: R4.6 历史消息截断

- **WHEN** 某会话已有超过 6 条历史消息
- **AND** 学生再次提问
- **THEN** 组装进请求的上下文**只含最近 6 条**消息
- **AND** 更早的消息**不进入**上下文

#### Scenario: R4.7 历史消息逐条截断 500 字

- **WHEN** 某条历史消息正文超过 500 字
- **THEN** 进入上下文时该条**被截断至 500 字**
- **AND** 库中该条消息原文**不被修改**

#### Scenario: R4.8 回答与引用落库

- **WHEN** 一次提问成功返回
- **THEN** `messages` 表新增**两条**：`role = 'user'` 一条（提问原文）、
  `role = 'assistant'` 一条（回答正文）
- **AND** assistant 那条的 `citations` 保存本次引用列表，`skill_name` 保存命中的技能名

### Requirement: R5 技能路由与流式作答

系统 SHALL 在**智能体绑定且已启用**的技能中做词法路由，**至多选中一个**；
检索 SHALL 只在智能体绑定的材料内进行，取前 4 条并按技能的「资料」范围过滤。
回答 SHALL 以 SSE 逐段下发，事件顺序为 `meta` → `delta*` → `done`（失败时为 `error`）。
`meta` 的 `status` SHALL 恒为 `ok`——**检索落空仍然作答**，此时引用列表为空且正文不含编号。

#### Scenario: R5.1 技能路由至多选一个

- **WHEN** 提问同时命中该智能体绑定的**两个**技能（各命中一个关键词）
- **THEN** 回答中**只出现一条**技能引导正文
- **AND** `meta.skill` 为其中**一个**技能名，不是多个

#### Scenario: R5.2 路由零命中不选技能

- **WHEN** 提问**未命中**任何候选技能的适用条件
- **THEN** 不选技能，`meta.skill` 为空串
- **AND** 回答**仍然生成**（不为空）

#### Scenario: R5.3 同分取编号较小者

- **WHEN** 两个候选技能**得分相同**且均被命中
- **THEN** 选中**编号较小**的那个

#### Scenario: R5.4 停用的技能不进候选集

- **WHEN** 某技能 `enabled = 0` 但仍被智能体绑定
- **AND** 提问命中该技能的适用条件
- **THEN** 该技能**不进候选集**，不被选中

#### Scenario: R5.5 检索只在绑定材料内

- **WHEN** 智能体只绑定了材料 M1，提问的内容在**本班另一份未绑定**的材料 M2 中也能命中
- **THEN** 检索结果**只含 M1** 的命中
- **AND** `citations` 中不出现 M2 的材料

#### Scenario: R5.6 引用前 4 条

- **WHEN** 智能体绑定的材料能命中 5 条以上
- **THEN** 送入上下文的命中**只有前 4 条**
- **AND** `citations` 的条目数与之一致

#### Scenario: R5.7 资料范围过滤

- **WHEN** 命中技能的「资料」字段非空（如「数学」）
- **THEN** 命中结果中**只保留标题含该词**的材料
- **AND** 引用编号连续无跳号

#### Scenario: R5.8 SSE 事件顺序

- **WHEN** 一次提问正常完成
- **THEN** 响应 `Content-Type` 为 `text/event-stream`
- **AND** 事件顺序为 `meta` → 一个或多个 `delta` → `done`
- **AND** 每个 `delta` 的 `text` 是**新增**文本，拼接起来等于最终回答

#### Scenario: R5.9 meta 恒为 ok 且带引用

- **WHEN** 一次提问正常完成
- **THEN** `meta` 事件的 `status` 为 **`ok`**
- **AND** `meta` 含 `skill` 与 `citations` 两个字段
- **AND** `citations` 的编号顺序与正文中的 `[n]` 引用顺序一致

#### Scenario: R5.10 检索落空仍作答且无编号

- **WHEN** 提问在绑定材料内**检索不到任何命中**
- **THEN** `meta.status` 仍为 `ok`，`citations` 为**空数组**
- **AND** 回答正文**不含** `[n]` 编号
- **AND** 回答**不是**整段固定文案「资料中未找到相关内容」——仍是一次正常的作答

#### Scenario: R5.11 生成失败时补 error 事件

- **WHEN** 流已开始（已发出 `meta`）后对话网关报错
- **THEN** 补发一个 `error` 事件
- **AND** HTTP 状态码**保持 200**（流已开始，不能再改状态码）
- **AND** `done` 事件**不发出**

#### Scenario: R5.12 资料不进系统消息

- **WHEN** 组装请求消息
- **THEN** 系统消息只含**角色提示词**、**技能说明**与边界句
- **AND** 检索到的资料正文**不进系统消息**，只作为数据进入用户消息一侧

### Requirement: R6 接口文档与可观测性

系统 SHALL 提供 OpenAPI 描述与 Swagger 页面，且两者**同源**——文档与处理器注解来自同一处定义。
接口文档 SHALL 覆盖本课新增的全部接口。

#### Scenario: R6.1 Swagger 页面可打开

- **WHEN** 浏览器打开 `GET /swagger/`（或 `GET /swagger`）
- **THEN** 返回 200，页面渲染出 Swagger UI
- **AND** 页面加载的描述文件指向本服务的 `GET /openapi.json`

#### Scenario: R6.2 OpenAPI 描述可获取

- **WHEN** 请求 `GET /openapi.json`
- **THEN** 返回 200，`Content-Type` 为 `application/json`
- **AND** 响应体是合法的 OpenAPI 文档（`openapi` 与 `paths` 字段存在）

#### Scenario: R6.3 文档覆盖本课接口

- **WHEN** 检查 OpenAPI 的 `paths`
- **THEN** 包含本课新增的接口：`/api/prompt`、`/api/skills`、`/api/skills/{id}`、
  `/api/agents`、`/api/agents/{id}`、`/api/conversations`、`/api/conversations/{id}`、
  `/api/conversations/{id}/messages`
- **AND** 每个接口标注了是否需要登录、以及错误码

#### Scenario: R6.4 文档路由不被业务路由遮蔽

- **WHEN** 在**未登录**状态下请求 `GET /swagger/` 与 `GET /openapi.json`
- **THEN** 两者均返回 200（文档本身不要求登录）
- **AND** **不是** 404 或 401

### Requirement: R7 验收脚本与接口测试

系统 SHALL 提供**一条命令**跑完全部验收的脚本，退出码即判定；
并 SHALL 提供按 URL 组织的接口测试目录与一键 runner，生成 HTML 报告。

#### Scenario: R7.1 一条命令跑完验收

- **WHEN** 执行 `bash scripts/verify-all.sh`
- **THEN** 脚本按序跑完各验收脚本，输出每项的**实际断言数**与**预期断言数**
- **AND** 实际断言数与预期**不符**时判定为失败（不只是显示出来让人自己注意）
- **AND** 全部通过时退出码为 0，有任一项失败时退出码为 1

#### Scenario: R7.2 断言数与预期值必须同步

- **WHEN** 任一验收脚本的断言数被改动
- **THEN** `verify-all.sh` 中对应的**预期值必须同步更新**
- **AND** 两者不一致时 `verify-all.sh` 判定失败（防止"跑过的都过了"掩盖"该跑的没跑"）

#### Scenario: R7.3 接口测试按 URL 组织

- **WHEN** 查看 `apitest/cases/` 目录
- **THEN** 目录层级与 URL 路径对应（`cases/api/login/POST.py` 表示 `POST /api/login`）
- **AND** 存在覆盖登录、检索、提示词、技能、创建会话并发消息的用例

#### Scenario: R7.4 接口测试不止对状态码

- **WHEN** 审阅接口测试用例
- **THEN** 至少有一类用例把**接口返回内容**与**数据库实际内容**做交叉核对
- **AND** 用例断言失败时以非零退出码体现

#### Scenario: R7.5 报告不入库

- **WHEN** 检查仓库的 Git 忽略配置
- **THEN** 接口测试生成的 HTML 报告与本地 `.env` **均被排除**
- **AND** 密钥只存在于本机 `.env`，不进入 Git

#### Scenario: R7.6 浏览器端到端覆盖第 5 课主路径

- **WHEN** 执行 `node scripts/e2e-tutor.mjs`
- **THEN** 覆盖教师配置（提示词 / 技能）、学生建会话、流式逐段渲染、引用材料可打开
- **AND** 全部断言通过时退出码为 0

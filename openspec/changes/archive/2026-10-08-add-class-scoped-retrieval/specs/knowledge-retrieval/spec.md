# spec delta: knowledge-retrieval

> **编号来源说明（2026-09-29 课件上线后重写）**：第 4 课课件已上线并镜像至
> `D:/code/Internet software development/lesson4/课件/第4课-课件-可追溯知识库检索/`。
> 本 delta 的**范围与选型取自课件原文**（逐条在 Requirement 下方标 `课件出处`），
> **R 编号体系、阈值以外的判定口径、以及"如何让一条 Scenario 可自动判定"属【我方定义】**
> （课件未给条款号，也未给验收脚本口径）。涉及迭代 1 横切约定的条目标注继承来源。
>
> 事实分级：**【课件原文】**＝镜像 HTML 文字；**【我方定义】**＝本仓补充；**【未验证】**＝本机无法实测者（见 R10.4）。

## ADDED Requirements

### Requirement: R1 检索接口与请求校验

系统 SHALL 提供检索接口 `POST /api/search`，支持 `mode` ∈ {`keyword`, `vector`, `hybrid`}，缺省为 `hybrid`；
该接口 MUST 经服务端会话鉴权。【课件原文 scope.html §2 / flow.html §3；鉴权继承 auth-upload R2】

#### Scenario: R1.1 未登录检索被拒

- **WHEN** 无有效会话 Cookie 的请求提交 `POST /api/search`（任一 mode）
- **THEN** 返回 401，响应体结构与迭代 1 其他受保护接口的 401 同形

#### Scenario: R1.2 空查询被拒

- **WHEN** 请求体缺 `query`、`query` 为空串或全空白
- **THEN** 返回 400【课件原文 flow.html §3：「空查询返回 400」】

#### Scenario: R1.3 非法 mode 被拒

- **WHEN** `mode` 取三者以外的值（含空串）
- **THEN** 返回 400，且错误响应结构与 R1.2 同形

#### Scenario: R1.4 top_k 越界被拒

- **WHEN** `top_k` 不是 [1, 20] 内的整数
- **THEN** 返回 400

#### Scenario: R1.5 缺省为混合模式

- **WHEN** 请求体不含 `mode`
- **THEN** 行为等同 `mode = "hybrid"`：两路都参与（由 R5.4 的融合断言验证）

#### Scenario: R1.6 合法请求返回结构

- **WHEN** 已登录用户提交 `{"query": "细胞周期", "mode": "hybrid", "top_k": 5}`
- **THEN** 返回 200，响应体为 `{"mode": "hybrid", "hits": [...]}`，`hits` 长度 ≤ `top_k`
- **AND** 每个 hit 携带 R3 定义的溯源字段

### Requirement: R2 检索侧的班级隔离

系统 SHALL 以**服务端会话中的 `class_id`** 收窄检索范围，且**关键字路与向量路都 MUST 携带该条件**，
回表取正文时 MUST 再次核对班级。【课件原文 class-scope.html §1–2】

#### Scenario: R2.1 三种模式下都只返回本班结果

- **WHEN** 班级 A 的会话分别以 `keyword` / `vector` / `hybrid` 检索一个**只有班级 B 的材料**才会命中的查询
- **THEN** 三次响应均为 200 且 `hits` 为空数组（本班无命中不是错误）
- **AND** 响应中不出现 B 班的 `material_id`、标题或正文片段

#### Scenario: R2.2 请求中携带的班级值被丢弃

- **WHEN** 请求体 / 查询串 / 请求头中携带 `"class_id": <B 班 ID>`，A 班会话检索本班可命中的查询
- **THEN** 返回 200，且响应与**不携带该字段**时的响应逐字段一致
- **AND** 不返回任何 B 班内容【课件原文 class-scope.html §1；语义沿用 auth-upload R4.7「忽略」】

#### Scenario: R2.3 教师与学生同界

- **WHEN** 同一班级的教师与学生分别提交同一查询（同 mode、同 top_k）
- **THEN** 两者返回的 `hits` 完全一致（检索能力无角色差异）

#### Scenario: R2.4 失败切片不参与检索

- **WHEN** 某材料存在 `index_status = 'failed'` 的切片（R4.2 构造），且其内容可被检索词命中
- **THEN** 任一 mode 的 `hits` 中都不出现该切片【课件原文：关键字路「仅检索 index_status = ready 的切片」；本仓扩展到三路】

#### Scenario: R2.5 向量路回表时再次核对班级（构造用例）

- **WHEN** 直接向 Qdrant 写入一个点：向量取自 B 班正文、payload 的 `class_id` 为 B 班、`chunk_id` 指向 B 班切片【我方定义的构造用例】
- **AND** A 班会话以 `vector` / `hybrid` 检索该向量对应的语义
- **THEN** `hits` 中不含该 `chunk_id`
- **AND** 若命中返回，其正文 MUST 来自 MySQL 中 A 班的切片行（payload 里的编号不得单独作为正文来源）【课件原文 class-scope.html §2】

#### Scenario: R2.6 跨班打开详情仍为 404

- **WHEN** A 班会话以 B 班 `material_id` 请求 `GET /api/materials/{id}`
- **THEN** 返回 404，且与不存在的 ID 返回的响应同形【继承 auth-upload R5 / 迭代 1 D3】

### Requirement: R3 每条命中可回溯到原文

系统 SHALL 在每个 hit 中携带溯源块：`material_id`、`material_title`、`chunk_id`、`chunk_index`、`snippet`、`start_offset`、`end_offset`、`score`，
缺任一字段视为不合格。【课件原文 scope.html §3「至少应给出材料标题、切片序号、字符区间与一段摘录」；字段集【我方定义】】

#### Scenario: R3.1 字段齐全且非空

- **WHEN** 任一检索返回非空 `hits`
- **THEN** 每个 hit 同时包含上述八个字段，且 `material_title` 非空、`snippet` 非空

#### Scenario: R3.2 摘录与偏移区间一致（可回原文）

- **WHEN** 该切片的上传**未启用预处理**（默认），以 `material_id` 取回材料正文，按字符（rune）取 `[start_offset, end_offset)` 区间
- **THEN** 该区间文本与 `snippet` **完全一致**
- **注**：启用预处理时偏移相对预处理后文本，此时本条不适用，改由 R9.6 判定

#### Scenario: R3.3 溯源可闭环跳转

- **WHEN** 以 hit 中的 `material_id` 请求 `GET /api/materials/{id}`（同一会话）
- **THEN** 返回 200 与该材料详情，且详情正文中可定位到 `snippet`

#### Scenario: R3.4 分数可复算

- **WHEN** 以 `vector` 模式返回非空 `hits`
- **THEN** 每个 `score` ∈ [0, 1] 且保留 4 位小数，`hits` 按 `score` 降序（并列规则见 R5.6）

### Requirement: R4 上传入库的切分、嵌入与索引状态

系统 SHALL 在上传成功后按本次策略切分正文、逐片嵌入并写入 Qdrant，**向量主键 MUST 等于 `knowledge_chunks.id`**；
切片正文存 MySQL，Qdrant payload 只存标识、不存正文。【课件原文 flow.html §2】

#### Scenario: R4.1 上传成功即入库完整

- **WHEN** 嵌入网关可用时教师上传一份合法 `.md`
- **THEN** `materials` / `knowledge_entries` 仍各新增一行（继承 auth-upload R4.1）
- **AND** `knowledge_chunks` 新增 ≥ 1 行，`index_status = 'ready'`
- **AND** Qdrant 中该材料的点数 = `knowledge_chunks` 中 `ready` 切片数，且每个点的 ID = 对应切片 `id`
- **AND** 每个点的 payload 仅含 `class_id` / `material_id` / `knowledge_entry_id` / `chunk_id` / `chunk_index`，**不含正文**【课件原文 flow.html §2】

#### Scenario: R4.2 嵌入失败时材料保留、切片标记失败

- **WHEN** 嵌入网关不可用（超时或非 2xx）时上传一份合法材料
- **THEN** 上传仍返回成功（材料已入库），`materials` / `knowledge_entries` 各新增一行
- **AND** 对应切片的 `index_status = 'failed'`
- **AND** Qdrant 中**不存在**这些切片的点（不写入不完整的向量数据）【课件原文 flow.html §2】
- **注**：这与既有 auth-upload R4.6 不冲突——R4.6 约束的是**材料与正文入库**阶段；切分/嵌入是其后的独立步骤（边界见 design D7）

#### Scenario: R4.3 切片覆盖完整且重叠可复算

- **WHEN** 任一材料的全部切片按 `chunk_index` 排序
- **THEN** 首片 `start_offset` = 0，末片 `end_offset` = 该次切分源文本的字符总数
- **AND** 相邻片满足 `chunk[i+1].start_offset < chunk[i].end_offset`（存在重叠）且区间单调推进不跳跃

#### Scenario: R4.4 重建索引按本次策略重切

- **WHEN** 教师以 `POST /api/materials/{id}/reindex` 指定另一策略
- **THEN** 旧切片行与 Qdrant 旧点先被删除，再按新策略写入（切片条数符合新策略，不残留旧主键）
- **AND** `knowledge_entries.body_text` 与上传时一致（未被改写）【课件原文 chunk.html §2 / flow.html §2】

#### Scenario: R4.5 学生重建索引被拒

- **WHEN** 学生会话请求 `POST /api/materials/{id}/reindex`
- **THEN** 返回 403，切片与向量无变化【继承 auth-upload R3.1】

### Requirement: R5 三种检索模式与融合排序

系统 SHALL 实现三条检索路径：`keyword` 只查 MySQL 全文索引、`vector` 只经 Qdrant、`hybrid` 为前两者按名次 RRF 融合；
`vector` 路 MUST 丢弃余弦相似度低于 **0.35** 的候选。【课件原文 flow.html §3 / rag.html 选型表】

#### Scenario: R5.1 关键字路命中原文且不调用嵌入网关

- **WHEN** 以 `keyword` 检索一个原文中直接出现的词
- **THEN** 返回 200 且命中该切片
- **AND** 本次请求期间嵌入网关的**调用计数不增加**（计数桩验证）【课件原文 flow.html §3「不调用嵌入服务」】

#### Scenario: R5.2 向量路命中语义相近内容

- **WHEN** 检索词与某切片原文无字面重叠但语义相近（如查「细胞分裂」命中含「有丝分裂」的切片）
- **THEN** 以 `vector` 检索时该切片出现在 `hits` 中

#### Scenario: R5.3 低于阈值的候选被丢弃

- **WHEN** 以 `vector` 检索时存在余弦相似度 < 0.35 的候选
- **THEN** 该候选不出现在 `hits` 中【课件原文 scope.html §2 / flow.html §3】

#### Scenario: R5.4 混合模式按名次融合

- **WHEN** 以 `hybrid` 检索，构造"两路都能命中、但各自的第一名不同"的查询
- **THEN** 两路都命中的切片排名**优于**只被一路命中的切片（RRF，`k = 60`）【课件原文 flow.html §3 / rag.html】

#### Scenario: R5.5 缺席的一路不贡献分数

- **WHEN** 以 `hybrid` 检索一个只有向量路能命中（关键字路落空）的查询
- **THEN** `hits` 的顺序与单独以 `vector` 检索时一致【课件原文 rag.html「缺席的一路不贡献分数」】

#### Scenario: R5.6 排序确定可复现

- **WHEN** 同一会话连续两次提交同一请求体（库数据未变）
- **THEN** 两次响应的 `hits` 完全一致（含顺序与分数）
- **AND** 分数并列时按 `chunk_id` 升序【我方定义，与迭代 1 验收可复现性同一纪律】

#### Scenario: R5.7 空库检索

- **WHEN** 本班没有任何 `ready` 切片时提交合法检索（任一 mode）
- **THEN** 返回 200 与空 `hits`（不是错误）

### Requirement: R6 问答接口与引用标注

系统 SHALL 提供 `POST /api/ask`：**先以混合模式取本班前 4 条切片**，无切片时 MUST 直接返回固定文案且**不调用对话网关**；
有切片时，对话网关收到的 MUST 只是材料标题、切片序号与切片正文。【课件原文 flow.html §3 / answer.html】

#### Scenario: R6.1 无命中时不调用对话网关

- **WHEN** 本班检索无候选切片（例如提问"今天天气如何"）
- **THEN** 返回 200，正文为固定文案「资料中未找到相关内容」，`citations` 为空数组
- **AND** 本次请求期间对话网关的**调用计数不增加**【课件原文 answer.html §2】

#### Scenario: R6.2 有命中时取前四条且顺序一致

- **WHEN** 本班存在 ≥ 5 条可命中切片，提交一次 `POST /api/ask`
- **THEN** `citations` 长度 = 4
- **AND** `citations` 的顺序与送交对话网关的切片顺序**逐项一致**（都以 R5 的融合名次为准）

#### Scenario: R6.3 送交网关的内容不含向量与他班材料

- **WHEN** 记录对话网关收到的请求体（计数桩留存最后一次请求）
- **THEN** 其中含材料标题、切片序号、切片正文与本轮提问
- **AND** **不含任何浮点向量分量**（grep 不到 `EMBEDDING_DIM` 长度的浮点数组）
- **AND** **不含本班以外的材料内容**

#### Scenario: R6.4 引用标注落在范围内

- **WHEN** 有命中且对话网关返回含 `[n]` 标注的回答
- **THEN** 每个出现的 `n` 都满足 1 ≤ n ≤ `len(citations)`（模型不得凭空引用）

#### Scenario: R6.5 客户端注入的 system 消息被丢弃

- **WHEN** 请求体携带 `system` 字段（任意内容）
- **THEN** 对话网关收到的 `system` 是服务端写入的那一份（与请求体内容不同）【课件原文 answer.html §2】

#### Scenario: R6.6 鉴权与空问题

- **WHEN** 无会话请求 `/api/ask`，或 `question` 为空串/全空白
- **THEN** 分别返回 401 / 400

#### Scenario: R6.7 不实现流式

- **WHEN** 提交合法 `/api/ask`
- **THEN** 响应 `Content-Type` 为 `application/json`，一次性返回，不带 `text/event-stream`【课件原文 answer.html §2「本课不实现流式输出」】

### Requirement: R7 失败语义与依赖隔离

系统 MUST 在依赖不可用时给出明确状态码，**不得编造相似度分数或回答**。【课件原文 flow.html §3；继承迭代 1 R8 健康解耦】

#### Scenario: R7.1 Qdrant 不可用时的降级

- **WHEN** Qdrant 不可达，分别以三种 mode 检索
- **THEN** `keyword` 仍返回 200 且可返回结果
- **AND** `vector` / `hybrid` 返回 **503**，且 `hits` 为空、不含任何分数【课件原文 flow.html §3】

#### Scenario: R7.2 健康检查不受新增依赖影响

- **WHEN** Qdrant 或两类网关不可达时请求 `GET /health`
- **THEN** 仍返回 200（不查库、不查 Qdrant、不查网关）【继承 auth-upload R8】

#### Scenario: R7.3 对话网关不可用

- **WHEN** 有命中切片但对话网关不可达
- **THEN** `/api/ask` 返回 503，`citations` 为空，**不返回部分回答**

#### Scenario: R7.4 向量维度不一致

- **WHEN** Qdrant 集合的向量维度与 `EMBEDDING_DIM` 不一致（模拟更换嵌入模型后检索旧数据）
- **THEN** 上传时对应切片标记 `failed`（R4.2），检索的 `vector` / `hybrid` 返回 503【我方定义；课件只要求「维度与 collection 保持一致」】

### Requirement: R8 检索配置外置

系统 SHALL 从环境变量读取 Embedding（`EMBEDDING_API_BASE` / `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` / `EMBEDDING_DIM`）、
对话（`CHAT_API_BASE` / `CHAT_API_KEY` / `CHAT_MODEL`）与 Qdrant（`QDRANT_URL` / `QDRANT_COLLECTION`）共九项，
缺任一项 MUST 启动失败；这些值 MUST NOT 出现在任何 API 响应、日志或前端产物中。【继承 auth-upload R7；Qdrant 二项【我方定义】】

#### Scenario: R8.1 缺配置启动失败

- **WHEN** 环境变量缺上述任一项时启动服务端
- **THEN** 进程以非零退出码终止，错误信息指明缺失项，且不监听任何端口

#### Scenario: R8.2 配置不出现在对外面

- **WHEN** 检查全部 API 响应、服务端日志与前端构建产物
- **THEN** 找不到 `EMBEDDING_API_KEY` / `CHAT_API_KEY` 的值（grep 级验证）

#### Scenario: R8.3 Qdrant 不暴露给客户端

- **WHEN** 检查 `deploy/compose.yaml` 与 Nginx 配置
- **THEN** Qdrant 端口**不映射**到宿主机、不经 Nginx 反代【课件原文 flow.html §1「客户端无法直连 Qdrant」；判定口径【我方定义】】

### Requirement: R9 切分策略与预处理

系统 SHALL 支持三种切分策略 `auto` / `custom` / `hierarchy`，缺省为 `auto`；
预处理 MUST NOT 改写 `knowledge_entries.body_text`。【课件原文 chunk.html】

#### Scenario: R9.1 缺省策略为自动窗口

- **WHEN** 上传或重建索引时未指定策略
- **THEN** 按 `auto` 处理：每片 ≤ 800 字、相邻片重叠 80 字【课件原文 chunk.html §1】

#### Scenario: R9.2 自动窗口优先在边界断开

- **WHEN** 上传一篇含句号与换行的正文（无显式策略）
- **THEN** 切片断点落在空行、换行或句号处（不在句子中间硬切），且不违反 R9.1 的长度约束

#### Scenario: R9.3 自定义策略参数生效

- **WHEN** 以 `custom` 指定长度 300、重叠 10% 并启用「移除 URL 与邮箱」
- **THEN** 切片长度 ≤ 300 字、重叠 ≈ 10%，且 `chunk_text` 中不含 URL
- **AND** 长度或重叠比例越界（长度 ∉ [100, 2000]、重叠 ∉ [0, 50]）时返回 400【课件原文 chunk.html §1】

#### Scenario: R9.4 按标题分章

- **WHEN** 对含 Markdown 标题的材料以 `hierarchy` 切分
- **THEN** 按 `#` / `##` / `###` 分层，标题文本保留在该章切片内；某章过长时再按 `auto` 规则切分【课件原文 chunk.html §1】

#### Scenario: R9.5 预处理不改写原文

- **WHEN** 上传时启用「移除 URL 与邮箱」或「折叠连续空白」
- **THEN** `knowledge_entries.body_text` 仍为上传时的原样（URL 仍在）
- **AND** 切片文本是预处理后的结果

#### Scenario: R9.6 偏移语义与预处理一致

- **WHEN** 上传时启用预处理
- **THEN** `start_offset` / `end_offset` 相对**预处理后的文本**计算（不再视作原文字符下标）【课件原文 chunk.html §2】
- **AND** 响应不承诺"按偏移可回原文"（R3.2 只在未启用预处理时适用）

### Requirement: R10 验收脚本化与未验证项显式化

系统 SHALL 提供 `verify-retrieval.sh` 并纳入 `verify-all.sh` 编排，断言数与预期值 MUST 一致方可通过；
**本机无法实跑的断言 MUST 以 SKIP 显式列出，不得计入通过数，也不得静默省略。**【继承迭代 1 硬判定纪律；SKIP 口径【我方定义】】

#### Scenario: R10.1 单脚本退出码即判定

- **WHEN** 库处于种子态、后端运行中，执行 `bash scripts/verify-retrieval.sh`
- **THEN** 退出码 0 表示全部**已执行**断言通过，非 0 即失败，日志落 `.verify-logs/`

#### Scenario: R10.2 纳入总编排且预期同步

- **WHEN** 执行 `bash scripts/verify-all.sh`
- **THEN** 编排含 `verify-retrieval.sh`，且总断言预期值 = 原有断言数 + 检索断言数（总数不符即红）

#### Scenario: R10.3 可重复执行

- **WHEN** 连续执行 `verify-retrieval.sh` 两次（中间不复位）
- **THEN** 第二次同样退出码 0（脚本不毒化自己的前置条件）

#### Scenario: R10.4 SKIP 清单固定且可核对

- **WHEN** 执行 `verify-retrieval.sh`
- **THEN** 输出的 SKIP 项**逐条对应**脚本顶部声明的「需真 Qdrant 的断言」清单，条数一致
- **AND** 该清单在验收报告里单独列为**未验证项**，不与已执行断言混计
- **说明**：本机 Docker 不可用、WSL 被安全策略禁止【实测 2026-09-29】，真 Qdrant 无法启动；
  这类断言涉及"Qdrant 真实检索行为"，本机只能做到**请求契约级**验证（见 design D2）

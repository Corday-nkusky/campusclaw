# Proposal: add-class-scoped-retrieval（CampusClaw 迭代 2 · 第 4 课）

> **修订说明（2026-09-29 晚）**：第 4 课课件上线（https://devops.hello1023.com/课件/第4课-课件-可追溯知识库检索/index.html ，
> 已镜像至 `D:/code/Internet software development/lesson4/课件/`）。本四件套按**课件口径重写**：
>
> | 项 | 本版（对齐课件） | 上一版（同目录、旧内容） |
> |---|---|---|
> | capability 名 | `knowledge-retrieval`（**课件点名**） | `retrieval` |
> | change 目录名 | `add-class-scoped-retrieval`（**沿用，未改**） | 同左 |
> | 向量库 | **Qdrant**（collection `campusclaw_chunks`，余弦） | MySQL JSON 列 + Go 暴力余弦 |
> | 检索模式 | **keyword / vector / hybrid 三模式**，默认 hybrid，RRF(k=60) | 仅向量 |
> | 问答 | **新增 `POST /api/ask`** | 明确不做 |
> | 嵌入失败 | 材料保留、切片标 `failed`、不写不完整向量 | 整体回滚 |
> | 请求里的 class_id | **丢弃**（沿用 auth-upload R4.7 的「忽略」语义） | 400 契约拒绝 |
> | 切分 | **三策略** auto / custom / hierarchy + 预处理 + 重建索引 | 固定 500/100 单策略 |
>
> 课件出处：capability 名 `knowledge-retrieval` 见 `scope.html`（「范围判定以 week04 中的 knowledge-retrieval 规约（spec）为准」）；
> 三模式与 Qdrant 见 `scope.html §2`、`rag.html 选型表`、`flow.html §2–3`；
> `/api/ask` 见 `flow.html §3`、`answer.html`；嵌入失败与重建索引见 `flow.html §2`、`chunk.html §2`；
> class_id 丢弃见 `class-scope.html §1`。
>
> **关于 change 目录名（2026-09-29 纠正一条我自己的错误）**：课件**没有规定 change 目录名**，
> 只规定了 capability 名 `knowledge-retrieval`。我此前据此推断并写入"change 应改名 `add-traceable-vector-retrieval`、
> 出处见 `index.html §1`"——**那条出处是假的**（课件全文无此文字），推断越界，已撤回。
> 目录名沿用 `add-class-scoped-retrieval`，以保持已交出的提交 URL 有效。

## Why

迭代 1（`auth-upload`，已归档）把「能登录、能隔离、能上传入库」做成了可验收的基线，
并在 Non-goals 里明文把向量检索留给了第 4 课：

> 「本迭代知识库只存可查正文，第 4 课再扩展检索。」

本迭代兑现这一条，且**按课件给出的范围兑现**：不只"搜出东西"，而是四件可判定的事——
**检索结果只来自本班、每条命中可回溯到原文切片、三种模式各自只访问该访问的组件、无依据时不凑数**。

## What Changes

- 新增能力 `knowledge-retrieval`：本班范围内的可追溯知识库检索，**三种模式**（`keyword` / `vector` / `hybrid`，默认 `hybrid`）。
- 上传链路扩展：正文入库后 → 切分写 `knowledge_chunks` → 逐片调**嵌入网关** → 写 **Qdrant**（向量主键 = `knowledge_chunks.id`）。
  **嵌入失败时材料记录保留、切片标记 `failed`、不写入不完整的向量数据**【课件原文 flow.html §2】
  （⇒ 与既有 auth-upload R4.1「双表新增」不冲突，边界见 design D7）。
- 新增检索接口 `POST /api/search`：会话鉴权；班级只取服务端会话；**关键字路与向量路都必须带班级条件，回表时再核对一次**；
  每条结果强制携带溯源块（材料 ID / 标题 / 切片序号 / 摘录 / 字符区间 / 分数），可经 `GET /api/materials/{id}` 回跳原文。
- 新增问答接口 `POST /api/ask`：**不是第三种检索**。它先用混合模式取本班**前 4 条**；一条都没有就在检索模块处直接返回固定文案
  「资料中未找到相关内容」、`citations` 为空，**且不调用对话网关**；有命中时才把材料标题、切片序号与切片正文交给对话网关，
  回答以 `[n]` 指回出处【课件原文 flow.html §3 / answer.html】。
- 新增重建索引接口 `POST /api/materials/{id}/reindex`（教师）：按本次请求的策略重建，先删旧切片与旧向量再写入【课件原文 chunk.html §2】。
- 数据层：新增 `knowledge_chunks` 表（含 `index_status`、`strategy`、`chunk_index`、字符区间，以及 **ngram 全文索引**用于关键字路）；
  **MySQL 不再存向量**，向量只存 Qdrant。
- Compose 新增 `qdrant` 服务（内部网络，不向宿主机暴露端口）。
- 新增配置项（Embedding 四项 + Chat 三项 + Qdrant 二项），全部 `.env` 外置，缺任一项**启动失败**——继承迭代 1 R7 横切约定。

## Impact

**代码**

| 区域 | 变化 |
|---|---|
| `backend/internal/config` | 新增 Embedding / Chat / Qdrant 配置段及启动校验（含 `EMBEDDING_DIM` 与 Qdrant collection 向量维度一致校验） |
| `backend/internal/db` | `knowledge_chunks` 建表 + `FULLTEXT ... WITH PARSER ngram`；`reset-demo-data.sh` 兼容 |
| `backend/internal/knowledge` | 切分（`auto`/`custom`/`hierarchy` + 预处理）、Embedding 客户端、Qdrant 客户端、检索编排（三路 + RRF）、对话网关客户端 |
| `backend/internal/materials` | 上传事务扩展：正文入库之外增加切分与向量化；新增 reindex 处理 |
| `backend/cmd/server` | 注册 `/api/search`、`/api/ask`、`POST /api/materials/{id}/reindex` |
| `frontend/src` | 检索入口（模式选择）+ 溯源结果列表 + 问答区与 `[n]` 出处列表 + 跳转高亮 |
| `deploy/compose.yaml` | 新增 `qdrant` 服务与数据卷 |
| `scripts/` | 新增 `verify-retrieval.sh`；`verify-all.sh` 追加该脚本并**同步更新总断言预期值** |
| 仓库根 | `.env.example` 追加 9 项 |

**数据**（新增 1 表）

`knowledge_chunks`：`class_id` 携带租户列约定（**NOT NULL + 索引**，与 `materials`、`knowledge_entries` 一致）；
`index_status` 区分 `ready` / `failed`（**关键字路只检索 `ready`**）；
`strategy` 记录本次切分策略；字符区间为 **rune 偏移**。

**接口（对外契约）**

| 方法与路径 | 鉴权 | 说明 |
|---|---|---|
| `POST /api/search` | 会话 | 本班检索，`mode` ∈ {keyword, vector, hybrid}（缺省 hybrid），`top_k` ∈ [1,20] |
| `POST /api/ask` | 会话 | 本班问答：命中才调对话网关，无命中返回固定文案 |
| `POST /api/materials/{id}/reindex` | 会话 + **教师** | 按请求策略重建该材料的切片与向量 |

其余接口契约**零变化**（`POST /api/materials` 上传的对外成功语义不变：材料与正文仍双表新增）。

**对外契约（全文一致）**：检索结果**天然不含**跨班条目——不是"过滤后隐藏"，而是两条路径的查询条件都在服务端以会话班级收窄。
跨班检索对外表现为 **HTTP 200 + 空结果**，不用 403/404 暗示"资料属于别的班"【课件原文 class-scope.html §3】，
与迭代 1「跨班 = 同形 404」在**按 ID 打开详情**这条路径上保持一致。

## Non-goals（明确不做）

| 不做项 | 理由 |
|---|---|
| **重排序（cross-encoder）** | 课件选型表明文「本课不实现」【课件原文 rag.html】 |
| **编排框架（LangChain / LlamaIndex / Dify）** | 课件明文不引入，由 Go 自行完成切分、嵌入与检索【课件原文 rag.html 选型表】 |
| **流式输出** | 课件明文安排在第 5 课【课件原文 answer.html §2】 |
| 多轮会话持久化 | 本课只做"此前若干轮可附于其后"，不做会话存储与历史管理 |
| PDF / Word / 图片解析 | 继承迭代 1 边界，仍只收 `.txt` / `.md` |
| 平台超级管理员 / 跨班检索入口 | 迭代 1 安全红线；一条可跨班的查询分支就已经绕过隔离 |
| JWT / OAuth / SSO、注册改密、K8s / CI / HTTPS / 多副本 | 迭代 1 边界不变 |
| 向量库分片、HNSW 调参、性能压测 | 课程量级（O(10²) 材料）不需要；本迭代不含扩展性任务 |

> ⚠️ **与迭代 1 Non-goals 的关系**：迭代 1 把「RAG / 向量检索 / 问答」列为不做（`AGENTS.md` 明确不做项）。
> 本迭代**正是解除这一条**——第 4 课的课题就是它。其余不做项（平台超管、JWT、PDF 解析、K8s 等）**继续有效**。
> `AGENTS.md` 已同步标注这一解除，避免 harness 自相矛盾。

## 技术栈

Go + MySQL 8.0（正文 / 切片 / **ngram 全文索引**）+ **Qdrant**（向量）+ React/Vite + Nginx；
Embedding 与对话两类模型网关走外部 HTTP（配置外置）。
**客户端不直连 Qdrant，也不直连两类网关**；密钥不下发浏览器。

决议过程、备选方案与否决理由，以及**本机无法实跑 Qdrant 的处理方式**见 `design.md`。

## 提交物形态

课程提交入口为**学期汇总仓** `isd-coursework`（2026-09-29 科代决议：GitHub 唯一公开仓），
本仓**不并入**学期仓；交付时**只把本 change 四件套（`proposal.md` / `design.md` / `tasks.md` / `specs/knowledge-retrieval/spec.md`）
拷贝到学期仓的 `campusclaw/openspec/changes/add-class-scoped-retrieval/` 并提交**，
第 4 课提交子路径 URL【我方定义】：

> ⚠️ **禁止再用 `git subtree`（2026-09-30 红线）**：`git subtree pull --prefix=campusclaw …` 会把**本仓全部源码**
> 灌进公开仓（2026-09-29 即因此把 130 个文件、含 backend/frontend/scripts/docs 全部推上了 GitHub，
> 之后不得不重写公开仓历史清理）。**公开仓只收交付物文档，不收源码**。

`https://github.com/Corday-nkusky/isd-coursework/tree/main/campusclaw/openspec/changes/add-class-scoped-retrieval/`

> 目录名**未随本次重写改变**【我方定义，理由：提交 URL 已按此路径交出，改名会使入口 404】。
> capability / spec delta 名仍为课件点名的 `knowledge-retrieval`（在 `specs/knowledge-retrieval/spec.md`）。
> 推送凭据【实测 2026-09-29】：不需要 `gh auth login`；git-credential-manager 里已存的 github.com 凭据有效，
> 直接 `git -c http.proxy= -c https.proxy= push origin main --tags` 即可。

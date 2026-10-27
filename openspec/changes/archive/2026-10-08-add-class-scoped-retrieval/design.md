# Design: add-class-scoped-retrieval

- 关联变更：`openspec/changes/add-class-scoped-retrieval/`
- Capability（spec delta）：`knowledge-retrieval` —— **课件点名的唯一命名**【课件原文 `scope.html`「范围判定以 week04 中的 knowledge-retrieval 规约（spec）为准」】
- 迭代：CampusClaw 迭代 2（第 4 课）
- 基线：`openspec/specs/auth-upload/spec.md`（R1–R10，本次**零修改**）
- 课件依据：`D:/code/Internet software development/lesson4/课件/第4课-课件-可追溯知识库检索/`（2026-09-29 镜像）

> **本版相对上一版（同目录、旧内容）的翻转**：上一版按课程页一句话（「限定班级范围的知识库检索，支持内容溯源，可以考虑使用向量数据库」）
> 否决了 Qdrant 与问答。课件上线后明确给出 **Qdrant + 三模式 + `/api/ask`**，本版**按课件改判**。
> 上一版的否决理由里，只有"**本机无 Docker，容器方案无法验收**"仍然成立（见 D2 与 D12）；
> 其余两条（隔离证据被拆成两半、量级不需要）被课件的显式选型覆盖。

> **目录名为什么没跟着改（2026-09-29 纠正）**：课件**只规定了 capability 名 `knowledge-retrieval`，
> 从未出现任何 change 目录名**【实测：镜像课件全文 grep 只有 `knowledge-retrieval` 一处命中，无 `add-*` 形式的 change 名】。
> 上一版我曾据课件推断 change 应改名 `add-traceable-vector-retrieval` 并写入文档，**该推断越界，已撤回**：
> 该名字不是课件要求，而是我的推定。因提交 URL 已按 `add-class-scoped-retrieval` 交出，
> 目录名**沿用旧名以保持提交入口稳定**，范围与实现仍完全对齐课件。

## 技术栈决议

**采用**：MySQL 8.0 存**正文与切片**（含 ngram 全文索引供关键字路）、**Qdrant** 存向量（collection `campusclaw_chunks`，余弦）、
Go 服务编排切分 / 嵌入 / 检索 / 问答；Embedding 与对话两类网关走外部 HTTP；
接口 `POST /api/search`、`POST /api/ask`、`POST /api/materials/{id}/reindex`；
信任边界不变（浏览器 → Nginx → Go → MySQL / Qdrant / 网关，**Qdrant 与网关不对客户端暴露**）。

---

## Decisions

### D1 检索形态：三模式检索 + 问答（课件要求）

**决议**：实现 `keyword` / `vector` / `hybrid` 三模式（默认 `hybrid`），并新增 `POST /api/ask` 问答接口。

**理由**：课件把两者都写进了范围——三模式见 `scope.html §2` 与 `flow.html §3`；
`/api/ask` 见 `flow.html §3`「问答接口再引入一个模块」与 `answer.html` 整页【课件原文】。
上一版以"课件没提问答"为由不做，**该前提已不成立**。

**验收上的保留**：对话模型"写得好不好"依然**不可硬判定**。因此 spec 只约束可判定的部分（R6）：
无命中不调网关、citations 顺序、送网关的内容不含向量与他班材料、`[n]` 落在范围内、system 由服务端写入。
**回答文本的质量不作断言**，写进验收报告的"人工确认项"，不混进脚本断言数。

### D2 向量库：Qdrant（推翻上一版的 MySQL JSON 列）

**决议**：向量存 Qdrant，集合 `campusclaw_chunks`，度量余弦；**向量主键 = `knowledge_chunks.id`**；
payload 只放 `class_id` / `material_id` / `knowledge_entry_id` / `chunk_id` / `chunk_index`，**不放正文**。

**理由**：课件选型表明文指定【课件原文 rag.html】，且 `index.html` 三条结论第一条就是"切片正文存 MySQL，向量存 Qdrant"。
上一版否决它的三条理由逐条处理：

| 上一版否决理由 | 本版处理 |
|---|---|
| 隔离证据会被拆成两半 | 不成立为否决：课件要求**两条路径都过滤 + 回表再核对**（`class-scope.html §2`），隔离证据反而更硬（spec R2.5 有构造用例） |
| 课程量级不需要近似索引 | 仍成立，但不是否决理由：本课要的是**选型一致**与"向量库"这一形态本身，不是性能 |
| 本机无 Docker，无法验收 | **仍成立** → 见 D12：真 Qdrant 断言以 SKIP 显式列出，另以 httptest 做**请求契约级**验证 |

**否决备选**：

| 备选 | 否决理由 |
|---|---|
| MySQL JSON 列 + Go 暴力余弦（上一版方案） | 与课件选型不一致；课件明确"向量库"与"向量主键 = 切片主键"的关联方式 |
| pgvector / Chroma / Milvus | 课件指定 Qdrant |
| 把 `chunk_text` 塞进 payload | 正文出现两份副本，"摘录取自 MySQL"这条溯源断言就失去意义 |

### D3 关键字路：MySQL FULLTEXT + ngram parser

**决议**：`knowledge_chunks.chunk_text` 上建 `FULLTEXT KEY ... WITH PARSER ngram`，
检索用 `MATCH(chunk_text) AGAINST(? IN NATURAL LANGUAGE MODE)`，并叠加
`class_id = 会话班级 AND index_status = 'ready'`。

**依据（MySQL 8.0 官方手册 14.9.8）**【官方原文】：

- ngram full-text parser 是 **built-in server plugin**，随服务器启动自动加载 → 不需要额外安装或配插件。
- `ngram_token_size = 2`【实测 2026-10-07：本机 MySQL80 `SHOW VARIABLES LIKE 'ngram_token_size'` 返回 2，正好等于课件要求的 token 长度 2】；它是**只读变量**，只能在启动参数或配置文件里改。
- 使用该 parser 时，`innodb_ft_min_token_size` / `ft_min_word_len` 一类最小词长选项**被忽略**。
- 停用词处理特殊：ngram parser 排除**包含**停用词的 token，默认停用词表是英文的；中文场景要准确需自建停用词表；**长度大于 `ngram_token_size` 的停用词被忽略**。

**为什么选 NATURAL LANGUAGE MODE 而非 BOOLEAN MODE**：课件要求关键字路"按全文相关度由高到低"（`flow.html §3`），
NL mode 把查询串转成 ngram 词项的**并集并按相关度排序**，直接给出可排序的分数；
BOOLEAN mode 会把查询串转成 ngram **短语搜索**（AND 语义），中文长问句容易整体落空。
顶 k 截断 + RRF 融合足以稀释并集带来的噪声。

**已知代价与【未验证】**：

1. 查询串短于 2 个字时生成不出 token → 关键字路返回空；此时 hybrid 由向量路单独贡献（符合课件"缺席的一路不贡献分数"）。
2. 默认英文停用词表里长度恰为 2 的词（如 `is` / `it`）可能误伤**中英混排**材料。**本课不自建停用词表**（课件未要求），列为已知代价。
3. **本机未实测**：MySQL80 服务当前未运行（连接报 `ERROR 2003`），`net start MySQL80` 返回"系统错误 5 · 拒绝访问"【实测 2026-09-29】。
   建表与检索行为**没有在本机跑过**，上述结论依据的是官方手册而非本机实测。实现第一步必须先按 R8.1 起库并实测 ngram 建表与检索。

### D4 切分：三策略 + 预处理 + 偏移语义

**决议**：实现 `auto`（≤800 字 / 重叠 80 字，优先在空行、换行、句号断开）、
`custom`（长度 100–2000、重叠 0%–50%，无断点处硬切，可移除 URL 与邮箱、折叠连续空白）、
`hierarchy`（按 `#` / `##` / `###` 分章，标题保留在章内切片，过长再按 auto 切）；缺省 `auto`。
偏移量按 **rune（字符）** 计。

**两条容易踩的规则**（课件 `chunk.html §2`）【课件原文】：

1. **预处理只作用于待切分与待嵌入的文本**，`knowledge_entries.body_text` 保持上传原样；
   因此启用预处理后，偏移是**相对预处理后文本**算的，**不能再当原文字符下标**。
   → 由此 spec 的 R3.2（"摘录可按偏移对回原文"）**只在未启用预处理时适用**；启用预处理时改由 R9.6 判定。
   这是课件口径与"溯源闭环"诉求之间唯一的张力点，本仓选择**如实分开写**，不假装偏移永远对得回原文。
2. **切换策略必须显式重建索引**：已入库材料不会自动重切；重建用的是本次请求的策略；先删旧切片与旧向量再写入，避免遗留过期主键。

### D5 溯源字段集

**决议**：`/api/search` 每个 hit 必带八字段：`material_id` / `material_title` / `chunk_id` / `chunk_index` / `snippet` / `start_offset` / `end_offset` / `score`。

**理由**：课件要求"材料标题、切片序号、字符区间、一段摘录，并支持打开对应材料"【课件原文 scope.html §3】。
八字段 = 材料（跳转）+ 切片（引用）+ 区间（定位）+ 分数（让排序可复算，供脚本硬判定）。
`score` 本身不溯源，但没有它，R5.6 的"顺序可复现"就无法断言。

### D6 班级隔离：两条路径都过滤 + 回表再核对

**决议**：`class_id` **只从会话上下文**取；查询串、JSON 正文、请求头里的班级值**解析后丢弃**（沿用 auth-upload R4.7 的"忽略"语义，**不用 400 拒绝**）；
关键字路 SQL 带 `class_id`、向量路 Qdrant filter 带 `class_id`、拿到向量主键后回 MySQL **再用同一班级条件**取正文。

**对外表现**：跨班检索 = **200 + 空 hits**（不用 403/404 暗示"资料属于别的班"）【课件原文 class-scope.html §3】；
按 `material_id` 打开详情仍沿用迭代 1 口径：跨班与不存在**同形 404**。

**为何从上一版的"400 契约拒绝"改为"丢弃"**：课件原文是"解析后一律丢弃"，且既有 auth-upload R4.7 也是"忽略"；
两种都能硬判定，但**课件口径优先**，且"丢弃"不向调用方泄露"你传的这个字段被我识别了"。

### D7 失败语义矩阵 + 与 auth-upload R4.6 的边界

| 场景 | 行为 | 依据 |
|---|---|---|
| 嵌入网关失败（上传时） | 材料与正文**保留**，切片 `index_status='failed'`，**不写 Qdrant** | 课件原文 flow.html §2 |
| Qdrant 不可达（检索时） | `keyword` 仍 200 有结果；`vector` / `hybrid` **503**，hits 空、不含分数 | 课件原文 flow.html §3 |
| 对话网关不可达（问答时） | `/api/ask` **503**，citations 空，不返回部分回答 | 【我方定义】 |
| 维度不一致 | 上传：切片标 failed；检索 vector/hybrid：503 | 【我方定义】 |
| `/health` | 不查库、不查 Qdrant、不查网关，恒 200 | 继承 auth-upload R8 |

**与既有 auth-upload R4.6「入库失败无残留」的边界**（重要，避免"文档打架"）：
R4.6 约束的是**材料与正文入库**这一阶段——那一阶段失败仍要清掉落盘文件、两表不留记录，**本变更不改它**。
切分 / 嵌入 / 写向量是**其后的独立步骤**：此时材料已合法入库，因此失败只标记切片状态、不回滚材料。
判定上两者不重叠：R4.6 用"上传非法文件 / 入库报错"触发，R4.2 用"嵌入网关不可达"触发。

### D8 问答接口与对话网关的边界

**决议**：`/api/ask` 自身不实现第三种检索。流程固定为：
仅以最新一句提问 → 混合模式取本班**前 4 条** → 无切片则**直接返回固定文案「资料中未找到相关内容」、`citations` 为空、不调网关** →
有切片才把**材料标题 + 切片序号 + 切片正文**交给对话网关 → 回答以 `[n]` 指回出处。

**红线**：

- 对话网关**拿不到向量分量**，也拿不到其他班级的材料。
- 客户端注入的 `system` **一律丢弃**，system 由服务端写入（只允许依据编号对应资料作答）。
- **本课不实现流式输出**（课件安排在第 5 课）。

**为什么"无命中不调网关"必须硬判定**：这是本课"不编造出处"的核心。若模型凭参数记忆作答，回答就脱离了本班材料，
整条溯源链失效。判定方式：把 `CHAT_API_BASE` 指向**计数桩**，断言调用次数为 0（见 D12）。

### D9 配置与密钥

**决议**：新增九项，全部 `.env` 外置，**缺任一项启动失败**（继承 auth-upload R7）：
`EMBEDDING_API_BASE` / `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` / `EMBEDDING_DIM`、
`CHAT_API_BASE` / `CHAT_API_KEY` / `CHAT_MODEL`、`QDRANT_URL` / `QDRANT_COLLECTION`。
启动时额外校验 `EMBEDDING_DIM` 与 Qdrant 集合的向量维度一致（不一致则启动失败并指明）。

**红线**：`QDRANT_URL` 只在服务端使用；compose 里 **Qdrant 端口不映射到宿主机**、不经 Nginx 反代（spec R8.3）。

### D10 与迭代 1 横切约定的交互

| 迭代 1 约定 | 本变更 |
|---|---|
| 访问控制必须在服务端 | 检索/问答/reindex 的班级与角色判定全在服务端；reindex 限教师（继承 R3.1） |
| 租户只取自会话 | 同上，请求里的班级值丢弃 |
| 跨班 = 同形 404（按 ID 打开） | 沿用；检索场景补充"跨班检索 = 200 空结果" |
| 密钥外置、缺项启动失败 | 扩展到九项 |
| `/health` 不查库 | 扩展到不查 Qdrant、不查网关 |
| 验收脚本化 + 断言硬判定 | 新增 `verify-retrieval.sh`，纳入 `verify-all.sh` 并同步预期值 |

### D11 排序确定性

**决议**：`hits` 按 `score` 降序；并列按 `chunk_id` 升序；`hybrid` 用 **RRF（k = 60）** 融合两路名次，缺席一路不贡献分数；
向量路先按余弦阈值 0.35 过滤。同一请求两次的结果必须逐字段一致。

**理由**：种子数据里必然出现并列（对称文本、重复材料）。不定并列规则，"第一条是谁"就不可复现，验收断言就失真。

### D12 本机无法实跑 Qdrant 的处理（诚实边界）

**事实**【实测 2026-09-29】：`docker` 不在 PATH 且 `C:\Program Files\Docker\` 默认路径不存在；
`wsl.exe` 被安全策略列入程序黑名单、无法启动（未做全盘搜索，但两条常规路径都已排除）。
→ **真 Qdrant 在本机起不来**。

**由此本机状态天然落在降级路径上**，可验证与不可验证的划分是：

| 项 | 本机可否验证 | 说明 |
|---|---|---|
| 关键字路（R5.1 / R2.4 / R9） | ✅ 可 | 只依赖 MySQL |
| Qdrant 不可用时的降级（R7.1） | ✅ 可 | 本机本就不可达，恰好是真实状态 |
| 嵌入失败的 failed 分支（R4.2） | ⚠️ 视网关可用性 | 若外部网关不可达，本机落的就是这个分支 |
| 无命中不调对话网关（R6.1） | ✅ 可 | 无 ready 切片时即该分支 |
| 正常路径：vector / hybrid / ask 有命中 | ❌ 需真 Qdrant + 网关 | 列 **SKIP** |
| Qdrant 查询是否真带 class_id filter | ⚠️ 契约级可验 | httptest 假 Qdrant 断言请求体 |
| compose 起四服务 | ❌ 需 Docker | 沿用迭代 1 §7.5 的留白口径，静态校验 + 未验证清单 |

**三条硬纪律**（写进 spec R10.4）：

1. SKIP 项必须有固定清单，脚本输出条数与清单一致；SKIP **不计入通过数**。
2. 契约级验证只证明"发给 Qdrant 的请求长什么样"，**不能替代**真库行为——报告里要分开写。
3. 一旦本机具备 Docker/WSL，SKIP 项必须转为实跑，不得长期挂 SKIP 交付。

---

## 数据模型

```sql
CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    material_id   BIGINT UNSIGNED NOT NULL,
    class_id      BIGINT UNSIGNED NOT NULL,
    chunk_index   INT             NOT NULL,
    chunk_text    TEXT            NOT NULL,
    start_offset  INT             NOT NULL,
    end_offset    INT             NOT NULL,
    index_status  ENUM('ready','failed') NOT NULL DEFAULT 'failed',
    strategy      VARCHAR(16)     NOT NULL DEFAULT 'auto',
    created_at    TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_chunks_material_index (material_id, chunk_index),
    KEY idx_chunks_class (class_id),
    KEY idx_chunks_status (index_status),
    FULLTEXT KEY ft_chunk_text (chunk_text) WITH PARSER ngram,
    CONSTRAINT fk_chunks_material FOREIGN KEY (material_id) REFERENCES materials (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

要点：

- `class_id` 遵循租户列约定（**NOT NULL + 索引**），与 `materials`、`knowledge_entries` 一致；冗余存储，隔离谓词不依赖 JOIN。
- `index_status` 默认 `failed`：**只有嵌入与写向量全部成功后才置 `ready`**，避免"半入库"被当成可用。
- `strategy` 记录本次切分策略，供 R4.4 / R9 判定"重建是否用了新策略"。
- **MySQL 不再存向量**（上一版的 `embedding` JSON 列与 `embedding_dim` 列取消）——向量只在 Qdrant。
- `knowledge_entries` **保持不动**：它仍是"材料全文可查正文"，详情页继续读它；`knowledge_chunks` 是检索层。
- ⚠️ `FULLTEXT ... WITH PARSER ngram` 依赖 ngram parser 可用（官方：内置插件随服务器自动加载）。
  若建表失败，须**启动失败并指明原因**，而不是静默退化成无全文索引（否则关键字路会静默全表扫描或报错）。

## 与上一轮 change 的关系

- `auth-upload` 规约**零修改**（无 MODIFIED / REMOVED）：上传的对外成功语义不变，
  本变更只在其后追加切片与向量，并在 R4.2 定义新增步骤的失败语义（边界见 D7）。
- 本变更 delta 只含 `knowledge-retrieval` 能力的 ADDED Requirements。

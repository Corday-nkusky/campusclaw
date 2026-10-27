# Tasks: add-class-scoped-retrieval（capability: knowledge-retrieval）

> **归档说明（2026-10-08）**：归档时 58/60 勾。两条未勾均为「明知而为」：
> - §11.4（归档本 change）——归档动作本身即其完成证明，按归档纪律不在归档目录回改勾选；
> - §12.3（换机从零复现）——依赖备用环境，列入验收报告 §5.1 未验证清单。
> 验收证据：`docs/验收报告-迭代2.md`（2026-10-08 复验定稿版：两轮 verify-all 全绿 193 断言 +
> 行为级探针实证，未验证清单单列）。归档后 `openspec validate --all --strict` 全绿。

> 进度载体**只有本文件的方括号**。没跑就勾 = 进度失真。
> 纪律：一个 task 一轮 = 实现 → 审查 diff → 运行 verify → 提交；**失败项不得带入下一项**。
> 前置条件：
> 1. MySQL80 **运行中**（⚠️ 2026-09-29 实测：服务未运行，`net start MySQL80` 因权限被拒 → 需先由科代启动）；
> 2. 迭代 1 基线成立（`bash scripts/verify-all.sh` 退出码 0）；
> 3. `.env` 已填 Embedding / Chat / Qdrant 九项（缺任一项服务端启动失败，见 R8.1）。
>
> **本机硬限制**：Docker 不可用、`wsl.exe` 被安全策略禁止【实测 2026-09-29】→ 真 Qdrant 起不来。
> 涉及真 Qdrant 的断言一律按 R10.4 走 SKIP，**不得静默省略、不得计入通过数**。

## 1. 配置与骨架（→ R8）

- [x] 1.1 `.env.example` 追加九项与注释：`EMBEDDING_API_BASE` / `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` / `EMBEDDING_DIM`、`CHAT_API_BASE` / `CHAT_API_KEY` / `CHAT_MODEL`、`QDRANT_URL` / `QDRANT_COLLECTION`；本机 `.env` 同步填入
- [x] 1.2 `backend/internal/config` 新增 Embedding / Chat / Qdrant 三个配置段：缺任一项启动失败（→ R8.1）；`EMBEDDING_DIM` 须为正整数
- [x] 1.3 启动时校验 Qdrant 集合向量维度 = `EMBEDDING_DIM`，不一致则启动失败并指明（→ R7.4 的前置拦截）
- [x] 1.4 config 单测：九项齐全 / 缺任一项 / 维度非正整数 三种情形的启动行为（→ R8.1）
- [x] 1.5 密钥不泄漏单测：日志与响应中不含 key 值（→ R8.2）

## 2. 数据层（→ R3、R4、R5、R9）

- [x] 2.1 **先实测 ngram 可行性**（D3 标记为【未验证】，必须最先闭环）：起库后建临时库验证 `FULLTEXT ... WITH PARSER ngram` 可建、中文可检索，并把 `SHOW VARIABLES LIKE 'ngram_token_size'` 的实际值记录进 design D3；若 ≠ 2，记录需改 my.ini 并重启的结论
- [x] 2.2 `backend/internal/db/schema.go` 新增 `knowledge_chunks` 建表语句（按 design 数据模型，含 `uk_chunks_material_index`、`idx_chunks_class`、`idx_chunks_status`、`ft_chunk_text`）
- [x] 2.3 建表失败（ngram parser 不可用）时须**启动失败并指明原因**，不静默退化（design D3 末段）
- [x] 2.4 `reset-demo-data.sh` 兼容新表：复位时清空 `knowledge_chunks` 并清 Qdrant 集合（Qdrant 不可达时打印警告而非失败）
- [x] 2.5 种子数据：两个班级各 ≥ 2 份材料，其中**B 班独有主题材料** ≥ 1 份（→ R2.1）、**对称文本材料** ≥ 1 份（→ R5.6 并列）、**中英混排材料** ≥ 1 份（暴露 ngram 停用词影响）
- [x] 2.6 种子材料启动时补齐索引（默认 `auto` 策略），与上传走同一条切分链路（→ R4.3）

## 3. 切分与预处理（→ R9、R4.3）

- [x] 3.1 切分函数：三策略 `auto` / `custom` / `hierarchy`，rune 级偏移，输出 `[]Chunk{Text, Start, End, Index}`
- [x] 3.2 单测：`auto` 800/80 与边界优先断开、`custom` 长度与重叠生效及越界 400、`hierarchy` 按标题分章且标题保留在切片内、「空材料 / 单行超长 / 无标题」四情形（→ R9.1–R9.4）
- [x] 3.3 预处理：移除 URL 与邮箱、折叠连续空白；断言 `knowledge_entries.body_text` 保持原样、`chunk_text` 为预处理结果、偏移相对预处理后文本（→ R9.5 / R9.6）
- [x] 3.4 切分模块**不**调用嵌入服务、**不**写 Qdrant（模块边界单测）

## 4. 嵌入与 Qdrant 客户端（→ R4、R2.5）

- [x] 4.1 Embedding 客户端：OpenAI 兼容 `/v1/embeddings`；校验返回维度 = `EMBEDDING_DIM`；超时与非 2xx 归一为可判定错误
- [x] 4.2 Qdrant 客户端：建集合（`campusclaw_chunks`，余弦，size = `EMBEDDING_DIM`）、upsert（**点 ID = `knowledge_chunks.id`**）、按 `class_id` 过滤查询
- [x] 4.3 **契约级单测（httptest 假 Qdrant）**：断言 collection 名、upsert 点 ID = 切片主键、payload 恰好是五个标识字段且**不含正文**、查询请求体的 filter 含 `class_id`（→ R2.5 / R4.1）
- [x] 4.4 维度不一致的单测：集合 size ≠ `EMBEDDING_DIM` 时 upsert 报错并归一为可判定错误（→ R7.4）

## 5. 上传链路与重建索引（→ R4）

- [x] 5.1 上传事务改造：正文入库 → 切分 → 逐片嵌入 → 写 Qdrant → 回写 `index_status='ready'`；**任一片失败则只标记该批 failed，不回滚材料**（→ R4.1 / R4.2，边界见 design D7）
- [x] 5.2 嵌入失败用例：材料与正文仍在、切片 `failed`、Qdrant 无对应点（→ R4.2）；可将 `EMBEDDING_API_BASE` 指向不可达地址复现
- [x] 5.3 `POST /api/materials/{id}/reindex`：先删旧切片与旧向量再按本次策略重写；`body_text` 不变（→ R4.4）
- [x] 5.4 学生请求 reindex → 403 且无副作用（→ R4.5）
- [x] 5.5 failed 切片不参与任何 mode 的检索（→ R2.4）

## 6. 检索接口（→ R1、R2、R3、R5）

- [x] 6.1 `POST /api/search` 路由 + 请求校验：`query` 非空、`mode` 三值且缺省 `hybrid`、`top_k` ∈ [1,20]（→ R1.2–R1.6）
- [x] 6.2 关键字路：`WHERE class_id = 会话 AND index_status='ready' AND MATCH(chunk_text) AGAINST(? IN NATURAL LANGUAGE MODE)`；**不调用嵌入网关**（计数桩断言 → R5.1）
- [x] 6.3 向量路：问句嵌入 → Qdrant 按 `class_id` 过滤 → **丢弃余弦 < 0.35** → 主键回 MySQL **再用 class_id 核对**取正文（→ R5.2 / R5.3 / R2.5）
- [x] 6.4 hybrid：两路各自过滤后 **RRF（k=60）** 融合；缺席一路不贡献分（→ R5.4 / R5.5）
- [x] 6.5 响应组装：八字段溯源块，`material_title` 一次 JOIN 取回（→ R3.1）
- [x] 6.6 排序确定性：score 降序、并列按 `chunk_id` 升序、同请求两次逐字段一致（→ R5.6）
- [x] 6.7 隔离断言：A 班会话带 B 班 `class_id`（body / query / header 三种载体）→ 结果与不带时一致（→ R2.2）；三模式下跨班独有词 → 200 空 hits（→ R2.1）
- [x] 6.8 溯源闭环：命中结果可 `GET /api/materials/{id}` 回跳并按偏移高亮（→ R3.2 / R3.3）

## 7. 问答接口（→ R6）

- [x] 7.1 `POST /api/ask` 路由：会话鉴权、`question` 非空（→ R6.6）
- [x] 7.2 检索前置：仅以最新一句、混合模式、本班、前 4 条；`citations` 顺序与送模型的切片顺序一致（→ R6.2）
- [x] 7.3 无命中：固定文案「资料中未找到相关内容」+ `citations` 空 + **对话网关调用计数为 0**（计数桩 → R6.1）
- [x] 7.4 送网关内容：含标题/序号/正文/提问，不含向量分量、不含他班材料（→ R6.3）
- [x] 7.5 引用校验：回答中的 `[n]` 必须落在 1..`len(citations)`（→ R6.4）
- [x] 7.6 客户端注入的 `system` 被丢弃（→ R6.5）；响应为 JSON 非流式（→ R6.7）
- [x] 7.7 对话网关不可达 → 503、citations 空、不返回部分回答（→ R7.3）

## 8. 失败语义与依赖隔离（→ R7）

- [x] 8.1 Qdrant 不可达：`keyword` 仍 200 有结果；`vector` / `hybrid` 503 且 hits 空、不含分数（→ R7.1）
- [x] 8.2 `/health` 不查库、不查 Qdrant、不查网关（→ R7.2）
- [x] 8.3 compose：`deploy/compose.yaml` 新增 `qdrant` 服务 + 数据卷，**端口不映射到宿主机**、不经 nginx 反代（→ R8.3）
- [x] 8.4 compose 静态校验（`verify-compose-config.sh` 扩展），实跑仍列未验证（需 Docker）

## 9. 前端（→ R3 溯源闭环）

- [x] 9.1 材料页新增检索框与**模式选择**（keyword / vector / hybrid，默认 hybrid）；结果渲染八字段溯源块
- [x] 9.2 点击结果跳转材料详情并按偏移区间高亮（rune 偏移 → 前端按码点切分对齐）
- [x] 9.3 问答区：展示回答与 `[n]` 出处列表，列表顺序与 `citations` 一致；无命中时展示固定文案
- [x] 9.4 构建产物 grep 验证：不含任何 `EMBEDDING_*` / `CHAT_*` / `QDRANT_*` 配置值（→ R8.2）

## 10. 验收脚本（→ R10）

- [x] 10.1 新增 `scripts/verify-retrieval.sh`：断言覆盖 R1–R9 中**本机可执行**的全部条目；顶部固定声明「需真 Qdrant 的断言」SKIP 清单
- [x] 10.2 SKIP 计数硬判定：实际 SKIP 条数 ≠ 清单条数即红；SKIP **不计入通过数**（→ R10.4）
- [x] 10.3 计数桩脚本：本地起一个记录请求次数的 HTTP 桩，用于「不调用嵌入网关」（R5.1）与「不调用对话网关」（R6.1）判定
- [x] 10.4 `verify-all.sh` 纳入 `verify-retrieval.sh`，**同步更新 run 调用中的总断言预期值**（硬判定，总数不符即红——迭代 1 纪律）
- [x] 10.5 连续两次执行退出码均为 0（→ R10.3）
- [x] 10.6 全套实跑：`bash scripts/verify-all.sh` 退出码 0，记录总断言数与 SKIP 清单；告一段落后 `reset-demo-data.sh --yes` 复位

## 11. 文档与归档准备

- [x] 11.1 `README.md` 增补：`/api/search`、`/api/ask`、`/api/materials/{id}/reindex` 三个接口契约；`.env` 新增九项说明；**偏移语义说明**（未启用预处理时才可对回原文，见 R9.6）
- [x] 11.2 `AGENTS.md` 同步：活动 change 改为本 change；「明确不做」里解除「RAG / 向量检索 / 问答」并注明其余仍有效（已随本四件套一并修改）
- [x] 11.3 验收报告：判定结论 + 逐条证据 + **未验证清单**（真 Qdrant 相关、compose 实跑、ngram 实跑、外部网关的网络依赖项，分类单列）→ `docs/验收报告-迭代2.md`
- [ ] 11.4 `openspec validate --all --strict` 通过后归档本 change（注意：归档目录 tasks.md 只加批注、不动勾选状态）

## 12. 第 4 课提交物：学期汇总仓（2026-09-29 形态）

> 最终形态：**学期仓 `isd-coursework` 为 GitHub 唯一公开仓**，交付时**只拷贝本 change 四件套**
> 到其 `campusclaw/openspec/changes/add-class-scoped-retrieval/` 并提交。除该学期仓外一切内容留本地（科代 2026-09-29 确认）。
>
> 🚫 **禁止 `git subtree`（2026-09-30 新增红线）**：subtree 会把本仓**全部源码**灌进公开仓。
> 2026-09-29 因此把 130 个文件推上 GitHub，随后被迫重写公开仓历史清理。
> 正确做法：`cp` 四个文件 → `git add` → `git commit` → 推送。

- [x] 12.1 学期仓 `isd-coursework` 已建好并推送——**已完成**；2026-09-30 已清理为「只含交付物」（14 个文件，历史重写，旧 tag 已删）
- [x] 12.2 ~~change 改名带来的同步~~ **已撤销（2026-09-29）**：课件**未规定 change 目录名**（只规定 capability `knowledge-retrieval`），
  故目录名沿用 `add-class-scoped-retrieval`，提交 URL 仍为 `.../changes/add-class-scoped-retrieval/`。
  推送凭据【实测】：无需 `gh auth login`，git-credential-manager 已存凭据有效；
  命令 `git -c http.proxy= -c https.proxy= push origin main --tags`（清掉未开启的全局代理 `127.0.0.1:10910`）
- [ ] 12.3 从零复现自检：换一台环境按 README 走一遍（→ 迭代 1 §9.4 的未尽事项，本迭代争取闭环）

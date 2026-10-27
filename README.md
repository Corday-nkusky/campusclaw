# CampusClaw · 迭代 1（auth-upload）

教学案例 CampusClaw 的迭代 1：**认证授权 + 材料上传入库 + 班级隔离**。

> **当前状态（2026-10-27，第 7 课交付）**：本仓已从迭代 1 长到"平台"形态，五项能力齐备 ——
> ① 多个 AI 应用（智能体）② 提示词模板（`{{变量}}` 渲染）③ 知识库检索
> ④ 内置技能 + **上传技能文件** ⑤ **外部工具 / MCP 接入**（含调用循环与审计）。
> 工程规范五类落成文件：`docs/standards/01..05`，常驻约束在 `AGENTS.md`。
> 本轮验证记录（含日志 JSON 化实测、分层依赖输出、前端 E2E 8/8）在 `docs/lesson-07/`。
> 下文 §1–§8 是各迭代累积的说明，**最新事实以 `openspec/` 与 `docs/` 为准**。

规约（契约）现已是 **`openspec/specs/auth-upload/spec.md`**（本轮 Archive 生成的长期真相源）；
本轮的四件套在 `openspec/changes/archive/2026-09-23-add-auth-rbac-class-knowledge/`。
**代码是规约的派生物**。本仓的实现状态以 `tasks.md` 的方括号为唯一真相源，不以本文件的描述为准。

---

## 1. 范围

本迭代交付以下能力（逐条可判定，见 `spec.md`）：

| 编号 | 能力 |
|---|---|
| R1 | 账号口令登录与登出；失败响应同形；登录签发 **access + refresh 双令牌**（access 为 JWT，走 `Authorization: Bearer`）；篡改 token 一律 401 |
| R2 | token 只承载 `user_id`；`role` / `class_id` 每次从服务端用户表读取 |
| R3 | 角色能力边界：教师可上传，学生只读 |
| R4 | 教师上传 `.txt` / `.md`，解析正文并与材料记录同事务入库 |
| R5 | 材料列表 / 详情 / 下载按会话班级隔离 |
| R6 | 幂等种子数据，含可区分的 A / B 两班 |
| R7 | 配置全部来自环境变量；必填项缺失即启动失败 |
| R8 | Docker Compose 可复现启动；`/health` 不查库 |
| R9 | Web：登录页 + 材料页；身份以 `/api/me` 为准；access 失效时**静默刷新** |
| R10 | 页面体验增强（不替代服务端鉴权） |
| R11 | **令牌刷新与吊销**：refresh 轮换 + 重放检测；登出/主动吊销 = 删库（add-refresh-token） |

**判定标准不是界面好不好看**，而是：能登录、能隔离、能上传入库、第三方能按本 README 从零复现。

本迭代的**实现进度**：步骤 1–6 已完成并实测；**步骤 7（Compose）配置已写出但未实跑**（本机未装 Docker）；
**步骤 8（验收）本机可验证部分已完成**，判定结论见 `docs/验收报告-迭代1.md`。
上传接口已实现（**不再是 501**）。

### 1.1 检索与问答（add-class-scoped-retrieval，已实现）

在 auth-upload 之上新增三个接口（均走 `Authorization: Bearer` 会话鉴权）：

| 方法与路径 | 鉴权 | 说明 |
|---|---|---|
| `POST /api/search` | 会话 | 本班知识库检索。`{"query","mode","top_k"}`，mode ∈ keyword/vector/hybrid（缺省 hybrid），top_k ∈ [1,20]；每条 hit 携带八字段溯源块（material_id / material_title / chunk_id / chunk_index / snippet / start_offset / end_offset / score） |
| `POST /api/ask` | 会话 | 问答：混合模式取本班前 4 条；**无命中返回固定文案「资料中未找到相关内容」且不调用对话网关**；回答以 `[n]` 指向 citations |
| `POST /api/materials/{id}/reindex` | 教师 | 按本次请求的策略重建该材料切片与向量：`{"strategy":"auto/custom/hierarchy",...}` |

数据层新增 `knowledge_chunks`（切片正文 + `index_status` + ngram 全文索引）；向量只存 Qdrant
（点 ID = 切片主键，payload 只含标识不含正文）。跨班检索对外表现为 **200 + 空 hits**。
偏移为 rune（字符）偏移：未启用预处理时可按区间对回原文（R3.2）；启用预处理后相对预处理文本（R9.6）。

配置新增九项（缺任一项启动失败，见 `.env.example`）：`EMBEDDING_API_BASE/KEY/MODEL/DIM`、
`CHAT_API_BASE/KEY/MODEL`、`QDRANT_URL/COLLECTION`。这些值不会出现在任何响应、日志或前端产物中。

证据（全部【实测】）：`verify-all.sh` 一条命令跑完 **210 项断言** ——
明细 `verify-auth` 21 / `verify-isolation` 25 / `verify-upload` 40 / `verify-retrieval` 21 /
`verify-health-decoupled` 19 / `verify-compose-config` 18 / `browser-verify` 19 /
`e2e-acceptance` 31 / `e2e-tutor` 16，逐脚本原始输出与两轮取证见
`docs/lesson-07/verify-all.txt`。其中 `verify-retrieval` 与 `e2e-tutor` 依赖课程网关的
token 额度：额度耗尽时脚本会先探测、打 `SKIP_REASON` 并以退出码 3 退出，门禁记成
「未跑（依赖不可用）」——**跳过不算通过**，也不会伪装成失败。进度以 `tasks.md` 方括号为准（**59/61**）。

> **未验证清单**（不埋着，写在这里）：① `docker compose up` / `down` 从未实跑（本机 Docker 未安装，
> 只有 `D:\tools\Docker Desktop Installer.exe` 安装包），18 项静态校验**只证明配置写对了、不证明跑得起来**；
> ② 同伴交叉验证未做（需他人仓库与预置账号）；
> ③ `/uploads` 不被静态暴露这条**仅在 Go 直连下实测过**，Nginx 环境下未验证。
> 逐条见 `docs/验收报告-迭代1.md` §7。

> 📍 **先看 `PROJECT-MAP.md`** —— 完整目录地图 + 每个文件/文件夹的用途 + 常见「找不到的东西」。
> 其余文档见 §9 文档索引。

---

## 2. 明确不做

以下为 `proposal.md` 的 Non-goals。**写了也不构成验收项**，任何实现若包含它们都属于越界，需要先改规约再删代码。

- 知识库问答、向量检索、RAG —— 隔离与入库尚未稳定时做问答会造成跨班数据泄露；第 4 课再扩展检索。本迭代知识库**只负责存正文**。
- 对话助手、作业布置与批改、成绩与错题本。
- ~~JWT / OAuth / SSO~~ —— **add-token-auth（2026-09-30）已按老师要求改为 JWT
  登录**（`Authorization: Bearer`，见 `openspec/changes/add-token-auth/`）；
  **add-refresh-token（2026-10-14）在其上加 refresh 双令牌 + 静默刷新 + 可吊销**
  （见 `openspec/changes/add-refresh-token/`）。OAuth / SSO 仍不做。
- 注册改密、多校多租户、验证码 / 邮箱 / 短信登录。
- PDF / Word / 图片解析、在线预览编辑 —— 首版只支持 `.txt` / `.md`。
- Kubernetes、CI、公网域名、HTTPS、多副本 HA。
- **平台超级管理员**。它天然跨班，会与「跨班返回同一个 404」的隔离验收**直接冲突**：先放进一个可合法跨班的角色，那个 404 就不再能作为隔离成立的证据。正确顺序是先让隔离可判定并验收通过，再单独开一次变更引入。

---

## 3. 部署形态：单实例

本迭代**只支持单实例部署**。

登录失败限流放在**单进程内存**里，没有共享状态（add-token-auth 后会话已是**无状态 JWT**，
服务端不存会话行；add-refresh-token 后 refresh 状态落在**既有 MySQL** 中，未引入 Redis）：

- 限流计数在**进程内存**里；多副本时每个副本各有一份限流计数，限流不再准确。
- refresh 行在 MySQL 是**共享**的，所以**未登录态的续期**多副本可正确工作；
  不准的只有限流。

因此**不要**水平扩展 `api` 服务。多副本需要引入共享存储（如 Redis），属后续迭代。

`db` 服务**不映射宿主端口**，`api` 服务**不单独暴露**，只有 `web` 映射宿主端口。信任边界落在 API 进程。

---

## 4. 跨班访问的返回码：404

**跨班访问与「资源不存在」返回完全同形的 404。**

- 状态码、响应体结构一致，**不含**标题、正文或磁盘存储路径；
- 选择 404 而非 403 的理由：403 会泄露「该资源存在但你不该看」，便于攻击者遍历扫描 ID；
- 排障不靠响应体，改走**服务端审计日志**。

本约定在 `spec.md`（R5.2）与本文件保持一致；实现层对应 `design.md` D3。

> 注意：正因为用了 404，本迭代**不能**引入任何合法跨班的角色（见第 2 节的平台超级管理员）。

---

## 4.5 认证：双令牌 + 静默刷新 + 可吊销（add-refresh-token）

登录成功后返回**两枚**令牌，业务请求只带 access：

| 令牌 | 形态 | 有效期 | 存哪 | 作用 |
|---|---|---|---|---|
| `access_token` | HS256 JWT（payload 仅 `user_id`/`iat`/`exp`） | `ACCESS_TTL_MINUTES`，默认 **15 分钟** | 前端 `localStorage` `cc_access` | 每个业务请求的 `Authorization: Bearer` |
| `refresh_token` | 不透明随机串（32 字节 `crypto/rand` → base64url） | `REFRESH_TTL_DAYS`，默认 **30 天** | 前端 `localStorage` `cc_refresh`；**服务端存 SHA-256 摘要** `refresh_tokens.token_hash` | 换新 access，**可吊销** |

```bash
# 刷新（不需要 Authorization 头）
curl -X POST http://127.0.0.1:18080/api/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refresh_token":"<refresh>"}'
# → {"access_token":"...","refresh_token":"...（已轮换）","token_type":"bearer","expires_in":900}
```

**四条关键性质**（每条都有 Scenario 与 design 依据）：

1. **轮换 + 重放检测**：每次刷新，旧 refresh 立即作废（写 `replaced_by`），返回新的。
   若收到一个**已被换过**的 refresh，判为疑似泄露 → **吊销该用户全部 refresh** + 401。
   前端用**模块级互斥 Promise** 串行化刷新，避免并发把正常刷新误判成重放。
2. **登出 = 删库**：`POST /api/logout` 带 `{"refresh_token"}`，删掉那一行（幂等，删不到也 200）。
   此后**换不出新 access**。但**已签发的 access 在其余下 `exp` 内仍有效**——无状态 access 的
   固有属性，窗口**最长一个 access TTL（默认 15 分钟）**，不是旧的 24 小时。
3. **主动吊销**：改密 / 禁用 / 疑似泄露时删该用户全部 refresh 行，**至多 15 分钟**内其彻底下线，
   **不需要改代码或重启进程**。
4. **库里不存原文**：只存 SHA-256 摘要，读到库也拿不到可用令牌（与第 3 课课件
   「保存会话标识的摘要值」一致）。

**明确不做**：access 仍**无状态**（不查吊销表，否则退化成不透明串）；refresh 仍存
`localStorage` 而非 `HttpOnly` Cookie（保持与既有 R9 一致，避免重新引入 CSRF 面）——
见 `openspec/changes/add-refresh-token/` 的 tasks §6.1。

---

## 5. 目录结构

```
campusclaw-corday/
├── PROJECT-MAP.md                ← ⭐ 完整项目地图（找东西从这里开始）
├── AGENTS.md                     ← harness 约定（纪律与安全红线）
├── README.md                     ← 本文件
├── .env.example                  ← 配置样例（列全部必填项，无真实值）
├── .env                          ← ⛔ 真实配置，gitignore + dockerignore 双忽略
├── .gitignore / .dockerignore
├── openspec/
│   ├── project.md
│   ├── specs/auth-upload/spec.md ← 长期真相源（Archive 生成：10 Requirement / 35 Scenario）
│   ├── changes/archive/2026-09-23-add-auth-rbac-class-knowledge/
│   │   ├── proposal.md  design.md  tasks.md   ← tasks.md 是进度唯一真相源（59/61）
│   │   └── specs/auth-upload/spec.md          ← R1–R10 的 delta
│   ├── changes/archive/2026-10-08-add-token-auth/     ← 单 JWT 登录（已归档）
│   └── changes/add-refresh-token/                     ← ⏳ 活动变更：access+refresh 双令牌
│       ├── proposal.md  design.md  tasks.md
│       └── specs/auth-upload/spec.md          ← R1/R9 MODIFIED + R11 ADDED
├── backend/
│   ├── cmd/server/main.go        ← 入口：读配置 → 建池 → **先监听** → 后台迁移与种子
│   ├── internal/config/          ← 环境变量：DB/种子/密钥 + ACCESS_TTL_MINUTES / REFRESH_TTL_DAYS
│   ├── internal/db/              ← 连接、六表 DDL、幂等种子、存活探测器（含种子材料落盘）
│   ├── internal/auth/            ← JWT 签发验签、refresh 签发/校验/落库、口令、限流、登录登出刷新与鉴权中间件
│   ├── internal/materials/       ← 列表/详情/下载/**上传**（步 5 已落地）
│   ├── internal/knowledge/       ← 切片/嵌入/Qdrant/混合检索/问答/流式会话/带工具的一轮调用
│   ├── internal/conversation/    ← 会话编排：取应用→路由技能→检索→组消息→调模型→落库→工具循环
│   ├── internal/{skills,agents,prompt,prompttpl}/ ← 技能、智能体、班级提示词、提示词模板库
│   ├── internal/mcp/             ← MCP 服务器管理、JSON-RPC 客户端、工具解析与执行、审计
│   ├── internal/obslog/          ← 一行一条 JSON 的日志与请求标识（第 7 课规范 05 的落地点）
│   └── uploads/                  ← 文件落盘处（gitignore，**不对 HTTP 暴露**）
├── frontend/
│   ├── vite.config.ts            ← dev server + /api 代理；**必须保留 host: '127.0.0.1'**
│   └── src/
│       ├── api.ts                ← 唯一交互出口（身份只来自 GET /api/me）；access+refresh 存取与静默刷新
│       ├── App.tsx               ← loading → anonymous / authenticated 状态机 + 页签 + 命令面板
│       ├── styles.css            ← 设计系统、深色模式、Markdown 正文样式、响应式
│       ├── lib/markdown.ts       ← markdown-it，**html:false 是唯一的 XSS 防线**
│       ├── components/           ← CommandPalette / UploadDialog / Toast / TutorChat /
│       │                            TutorConfig（提示词/模板/技能/智能体）/ McpPanel（MCP 工具）
│       └── pages/                ← LoginPage / HomePage（工作台）/ MaterialsPage（材料页）/
│                                    KnowledgePage（知识检索）/ TutorPage（解题助手）
├── apitest/                      ← 第 5 课接口测试：一脚本一接口 + 一键跑 + HTML 报告（见 apitest/README.md）
├── deploy/                       ← 步 7：api.Dockerfile、web.Dockerfile、nginx.conf、
│                                    compose.yaml、README.md（用法 + 验证清单 + 未验证项）
├── scripts/
│   ├── init-db.sh / dev-run.sh               ← 建库与启动
│   ├── restart-backend.sh                    ← 复位后重启后端，让种子切片重建
│   ├── verify-all.sh                         ← ⭐ 一条命令跑完全部验收
│   ├── verify-auth.sh / verify-isolation.sh / verify-upload.sh / verify-retrieval.sh
│   │                                         ← 验证：回答「对不对」（21 / 25 / 40 / 21 项）
│   ├── verify-health-decoupled.sh            ← R8.4：/health 不查库（场景 A/B，19 项）
│   ├── verify-compose-config.sh              ← 步 7 配置的静态校验（17 项，**不证明跑得起来**）
│   ├── check-docs-freshness.sh               ← 文档状态数字与真相源对账（发布门禁）
│   ├── tcp-proxy.mjs                         ← 供上面那个脚本制造「运行中 DB 掉线」
│   ├── reset-demo-data.sh                    ← 把演示数据复位到种子态（幂等，自增值也拨回）
│   ├── e2e-acceptance.mjs                    ← 真实 Chrome：主路径 + 失败路径（31 项 + 11 张截图）
│   ├── e2e-tutor.mjs                         ← 真实 Chrome：解题助手配置/建会话/流式/引用（16 项）
│   ├── e2e-platform.mjs                      ← 真实 Chrome：模板/技能上传/MCP 三块新入口（8 项）
│   └── browser-verify.mjs / capture-*.mjs ×2 ← 前端验证（19 项）与取证（报文 + 截图）
├── mcp-server/                   ← 示范 MCP 服务器（独立 go.mod：/health + /mcp，两个工具）
└── docs/                         ← 作业答案、取证报告、截图、验收报告、进度与运维文档
    ├── standards/01..05          ← ⭐ 五类工程规范（第 7 课）
    ├── lesson-07/                ← ⭐ 第 7 课验证记录与原始输出（deps/checks/e2e/obslog）
    └── evidence/                 ← 真实报文证据（LLM 请求体逐字、SSE、MCP 审计）
    └── 验收报告-迭代1.md          ← ⭐ 判定结论 + 逐条证据 + **未验证清单**
```

完整版（含每个文件的一句话用途、常见「找不到的东西」）见 **`PROJECT-MAP.md`**。

---

## 6. 启动

### 6.1 标准方式（Docker Compose）

```bash
cp .env.example deploy/.env       # 再补 MYSQL_ROOT_PASSWORD
docker compose -f deploy/compose.yaml up -d
# 浏览器打开 http://127.0.0.1:8080
```

三服务：`web` / `api` / `db`，**只有 `web` 映射宿主端口**。细节与验证清单见 `deploy/README.md`。

> ⚠️ **本机没装 Docker，`up` / `down` 从未实跑过**（只有 `D:/tools/Docker Desktop Installer.exe`
> 安装包，未安装）。配置本身经静态校验 17/17 通过，但**静态校验只证明配置写对了、
> 不证明跑得起来**。实跑留证待补，见 `docs/验收报告-迭代1.md` §7。

### 6.2 本机开发直跑 —— ⚠️ 非发布形态

仅用于开发阶段验证，**不构成发布形态**，不作为交付验收依据。

```bash
# 1. 准备配置
cp .env.example .env
#    填写 SESSION_SECRET（openssl rand -hex 32）、DB_PASSWORD、
#    SEED_TEACHER_PASSWORD、SEED_STUDENT_PASSWORD
#    DB_HOST 用 127.0.0.1，DB_PORT 用 3306

# 2. 建库与应用账号（用 MySQL 管理员身份）
./scripts/init-db.sh <root口令> <应用账号口令>

# 3. 编译并启动（后端 + 前端一起，Ctrl+C 一起停）
bash scripts/dev-up.sh

#    只想单独跑后端：
cd backend && go build -o bin/server.exe ./cmd/server
cd .. && set -a; . ./.env; set +a; ./backend/bin/server.exe
```

**本仓开发用 `API_PORT=18080`，不是 8080。** 8080 是大量服务的默认端口，本机容易被
别的程序先占上；一旦前端代理打到不是本仓后端的进程，表现就是：前端能打开、一登录就报
「账号或口令不正确」。`.env.example` 里已按 18080 写，前端代理跟随同一个值。
部署形态不受影响：`deploy/compose.yaml` 里容器内的 `API_PORT` 写死 8080，只有 `web` 映射宿主端口。

```bash
# 只想登录/看界面、不想消耗嵌入额度（课程网关按天限 token）：
INDEX_ON_STARTUP=0 bash scripts/dev-up.sh
```

建表与种子在启动时**自动执行**，且幂等——重复启动不会复制用户或材料，也不会覆盖教师已上传的内容。
种子除两班账号与材料外，还给 A 班配了一套**开箱即用的助手**（技能「解题引导」+ 模板「学科助教骨架」
+ 智能体「生物助教」，绑定本班全部种子材料）：新库一起动，学生就能在解题助手页提问，
教师配置页三块也都有真实条目可看。教师可以随意改动或删除这套配置，重跑不会还原。

> **启动顺序是有意这样排的**：进程**先开始监听**，建表与种子在后台执行。所以 DB 慢或挂掉时，
> 你会先看到日志 `⚠️ 数据库暂不可用`，此时 `/health` **仍返回 200**，而 `/api/*` 返回 **503**，
> 后台每 5 秒重试一次；DB 一变可用，最多 5 秒内完成初始化。
> 这是 R8.4 的要求，也让编排系统不会因为 DB 抖动就重启容器。
> （先前「等库就绪再监听」的写法会让 `/health` 在 DB 挂掉时连 TCP 都建不起来，curl 返回 `000`，
> 实测确认后已改。）验证：`bash scripts/verify-health-decoupled.sh`。

### 6.3 前端开发服务器

`scripts/dev-up.sh` 已经把这一步包进去了；只想单独起前端时：

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173
```

前端代理的后端地址读 `.env` 的 `API_PORT`（默认 18080），不再写死 8080。

**为什么要配代理而不是给后端加 CORS**：让浏览器全程只看到单一来源（5173），
跨域问题就不存在；token 放 `Authorization` 头后也不再有「凭证跨域携带」的特例需求。
这与生产形态（Nginx 同时托管静态资源并反代 `/api`、`/health`）一致。
**不要**为了本地联调给 Go 加 CORS 中间件——那会把开发期的便利固化成生产配置。

两个已知细节：

- 已在 `vite.config.ts` 里**显式绑 IPv4 回环**（`host: '127.0.0.1'`）。
  Vite 默认只绑 IPv6 回环（`[::1]`），那种情况下 `http://127.0.0.1:5173` 直接连不上
  （curl 返回 `000`，实测）；绑 IPv4 后 `localhost` 与 `127.0.0.1` **两个地址都能用**
  （实测均 200）。绑的是回环地址，不是 `0.0.0.0`，不对外暴露。
- 经代理的响应会多出 `Vary: Origin` / `Access-Control-Allow-Origin`，
  那是 **Vite 开发服务器**加的，不是我们的 Go 后端（直连 8080 无这两个头，实测）。

---

## 7. 预置账号

| 账号 | 角色 | 班级 |
|---|---|---|
| `teacher_a` | 教师 | A 班 |
| `student_a1` | 学生 | A 班 |
| `student_b1` | 学生 | B 班 |

口令来自环境变量 `SEED_TEACHER_PASSWORD` / `SEED_STUDENT_PASSWORD`，落库为 **bcrypt 哈希**。仓库中不含任何真实口令。

两班的种子材料标题**故意可区分**（前缀 `A班-` / `B班-`），因为跨班访问失败必须靠对比才能证明。

---

## 8. 验证

### 8.1 一条命令跑完全套（推荐）

前置：后端 8080 在跑（`bash scripts/dev-run.sh`）、前端 5173 在跑（`cd frontend && npm run dev`）、
MySQL 在跑、`.env` 已填。

```bash
bash scripts/verify-all.sh
```

它按**正确顺序**（这个顺序不是随意的：几个脚本会真写数据，而有的脚本要求库处于种子态）
跑完 9 套验证并打印汇总表，退出码即判定：**0 = 全绿**。日志落在 `.verify-logs/`。

| 脚本 | 断言数 | 覆盖 |
|---|---|---|
| `verify-auth.sh` | 21 | R1.1–R1.5 / R2.1 / R8.4 / R9.2（认证与 token） |
| `verify-isolation.sh` | 25 | R5 / R6（班级隔离，含反向对照） |
| `verify-upload.sh` | 40 | R3.1–R3.2 / R4.1–R4.7 / R9.2（上传入库，含负面对照）+ BOM 归一化 |
| `verify-retrieval.sh` | 21 | R1–R7（班级范围检索问答；需嵌入网关与索引就绪） |
| `verify-health-decoupled.sh` | 19 | R8.4（`/health` 不查库，场景 A 起不来 / B 运行中掉线） |
| `verify-compose-config.sh` | 18 | `deploy/` 配置静态校验（**不证明跑得起来**） |
| `browser-verify.mjs` | 19 | 前端登录流程（真实 Chrome + CDP；token 落位 / 无 Cookie） |
| `e2e-acceptance.mjs` | 31 | 主路径四条 + 失败路径三条 + 界面语义 + XSS + 体验项 |
| `e2e-tutor.mjs` | 16 | 第 5 课解题助手（真实 Chrome：配置 / 建会话 / 流式 / 引用） |

**合计 210 项**（= 21+25+40+21+19+18+19+31+16）。真实累计基线见
**`docs/课程进度与后续路线.md`**（2026-10-10 实测 193/193，第 5 课接入 `e2e-tutor` 后为 210）。
迭代 1 当轮的判定结论与**未验证清单**见 **`docs/验收报告-迭代1.md`**（当轮 172）。

### 8.2 单独跑某一个

```bash
# 认证（变量名用 CC_USER：Windows 有同名系统变量 USERNAME，会取错值）
PASSWORD=<教师口令> bash scripts/verify-auth.sh
CC_USER=teacher_a PASSWORD=<教师口令> bash scripts/verify-auth.sh
```

其中 **R1.3**（add-token-auth 版）是验签强验证：对已签发 token 分别做
「改 payload（user_id→999）」「改签名末字节」「alg=none」三种篡改，断言全部 401。
无状态 JWT 下不存在会话固定攻击面（token 由服务端签名生成，客户端无法预构），原
「换发会话 ID」场景随之移除（design.md D7）。

> ⏳ **add-refresh-token 后本节尚未同步**：`verify-auth.sh` 仍按单令牌断言（19 项）。
> 双令牌的断言（refresh 可换新 access / 旧 refresh 重放 → 401 且全部吊销 / 登出后 refresh 失效）
> 属 Apply 阶段的"脚本同步"任务，与其断言数的预期值必须一并改。本 README 的断言表
> （§8.1）同样是**同步前的旧数字**。

> 它**故意连打 5 次错误口令触发限流**（R1.2 要验的就是这个）。为了不毒化主实例的账号状态，
> 脚本会**自己起一个监听 `:8092` 的一次性实例**来跑，跑完即 kill —— 所以**可以反复重跑**，
> 主实例上的 `teacher_a` 不会被锁。

**班级隔离**（步 4 的核心证据）：

```bash
bash scripts/verify-isolation.sh          # 25 项，实测全通过
```

含**反向对照**：A 班搜不到 B 班标题（`count=0`）之外，**B 班搜同关键词必须命中**（`count=1`）——
否则「搜索恒不匹配」会伪装成「隔离生效」。另比对跨班 / 不存在 / 非法 ID 三个 404 **逐字节相同**。

**上传入库**（步 5）：

```bash
bash scripts/verify-upload.sh             # 40 项，实测全通过
```

含负面对照：学生上传 403 后 `materials` / `knowledge_entries` 两表行数与上传目录文件数**逐项比对不变**；
超限 → 413；非 UTF-8 → 400；`.exe` → 400；`UPPER.MD` 大小写归一化后**接受**；
文件名带路径穿越后 `uploads/` 外**无新文件**；表单里塞 `class_id=2` **被忽略**（落库仍是会话班级）。

**前端验证与取证**（真实 Chrome + CDP，需前后端都在跑）：

```bash
node scripts/e2e-acceptance.mjs --teacher-pass '<口令>' --student-pass '<口令>'  # 31 项 + 11 张截图
node scripts/browser-verify.mjs --user teacher_a --pass '<口令>'                 # 21 项断言 + 截图
node scripts/capture-download-auth.mjs --user teacher_a --pass '<口令>'          # 下载鉴权 8 场景定格
node scripts/e2e-platform.mjs --teacher-pass '<口令>'                            # 第 7 课新增三块入口（8 项）
```

`e2e-platform.mjs` 全程**走界面**（不绕过 UI 调接口）：新建含 3 个变量的模板 → 试渲染 →
上传一个 `.md` 技能文件 → 建智能体绑模板并填变量 → 登记 MCP 服务器并连接看工具清单。
它需要一台在跑的示范 MCP 服务器（`cd mcp-server && PORT=18090 go run .`）。

前两个回答「**对不对**」（断言 + 退出码），后两个 `capture-*.mjs` 回答「**到底是什么**」
（真实请求头、响应体、带标注条的截图）。交「系统运行截图」类作业时后者更管用。

> `e2e-acceptance.mjs` 里有两处**取巧必须说清楚**，否则读者会以为那是「真点了出来」：
> ① 上传进度：本机回环是毫秒级，进度条根本来不及被看到 —— 用 CDP 的
> `Network.emulateNetworkConditions` 把上行限到 30 KB/s（**网络层限速，不是改界面**），
> 载荷约 300 KB；② 403/404 界面语义：学生界面上没有能点出 404 的入口 —— 用 CDP Fetch 域
> 把响应改写成**服务端真实返回的那个响应体**（与另一处实测逐字相同）再放行，
> 测的是前端收到该状态码时的行为，不是伪造结论。

> 详细用法、端口、账号、环境变量与排障见 `docs/开发环境与运维手册.md`。

### 8.3 规约结构校验（发布门禁）

**归档前**（`changes/` 下有活动变更时）：

```bash
npx --yes @fission-ai/openspec@latest validate add-auth-rbac-class-knowledge --strict
# → Change 'add-auth-rbac-class-knowledge' is valid      退出码 0
```

**归档后**（就是现在的状态，活动变更已清零）：change 名已经不存在了，裸 `validate --strict` 会
以退出码 **1** 报 `Nothing to validate`。要校验的对象换成了长期真相源：

```bash
npx --yes @fission-ai/openspec@latest validate --all --strict
# → ✓ spec/auth-upload          Totals: 1 passed, 0 failed (1 items)      退出码 0

npx --yes @fission-ai/openspec@latest list
# → No active changes found.                                            退出码 0
```

三条实测（2026-09-23，CLI v1.13.1）。三个坑：包名是 `@fission-ai/openspec`
（npm 上没有裸名 `openspec`，前置 Node ≥ 20.19.0）；**取退出码时不要接管道** ——
`cmd | tail` 之后 `$?` 是 `tail` 的退出码，恒为 0；`validate` 不带子命令且无活动变更时**不是 0**。

> ⚠️ **`validate` 通过 ≠ 实现合于规约**。一份格式完美但一行代码都没写的规约，`validate` 同样通过。
> 真正的判定在 `verify-*.sh` 与 `e2e-acceptance.mjs` 里逐条跑 Scenario。

---

## 9. 文档索引

| 文件 | 作用 |
|---|---|
| `PROJECT-MAP.md` | ⭐ **项目地图** —— 完整目录结构与各文件用途 |
| `openspec/specs/auth-upload/spec.md` | ⭐ **长期真相源**：R1–R10 Requirement 与 35 个 Scenario（Archive 生成） |
| `openspec/changes/archive/2026-09-23-add-auth-rbac-class-knowledge/proposal.md` | 范围、影响面、Non-goals |
| `openspec/changes/archive/2026-09-23-add-auth-rbac-class-knowledge/design.md` | 技术栈决议、D1–D11、四项 ADR |
| `openspec/changes/archive/2026-09-23-add-auth-rbac-class-knowledge/tasks.md` | 本轮进度（唯一真相源，59/61） |
| `openspec/changes/add-refresh-token/` | ⏳ **活动变更**：access+refresh 双令牌（proposal/design/tasks + spec delta） |
| `docs/验收报告-迭代1.md` | ⭐ 判定结论「本机可验证部分 166/166」+ 逐条证据 + **未验证清单** |
| `deploy/README.md` | Compose 用法、验证清单、未验证项 |
| `AGENTS.md` | harness 纪律与安全红线 + **五类工程规范索引**（第 7 课） |
| `docs/standards/01-目录与分层规范.md` | ⭐ 分层与依赖方向、外部依赖落点、新增功能落哪层 |
| `docs/standards/02-前后端开发规范.md` | ⭐ 前端唯一出口与三态、后端错误码表、校验位置、分页排序 |
| `docs/standards/03-测试规范.md` | ⭐ 三类用例、数据隔离、一键命令与产物位置、断言纪律 |
| `docs/standards/04-安全规范.md` | ⭐ 默认拒绝、密钥边界、输入输出、依赖检查频率 |
| `docs/standards/05-日志与观测规范.md` | ⭐ 一行一条 JSON、request_id、字段命名、迁移进度 |
| `docs/lesson-07/验证记录.md` | ⭐ 第 7 课核查清单逐条证据 + 诚实边界 |
| `openspec/changes/add-mcp-tool-bridge/` | ⏳ **活动变更**：外部工具与 MCP 接入（34 条 Scenario） |
| `docs/课程进度与后续路线.md` | 现在处于课程哪个位置、接下来还要干什么 |
| `docs/开发环境与运维手册.md` | 起停服务、端口、账号、环境变量、排障速查 |
| `docs/作业总览-0923.md` | 本次课程 6 道作业的状态总表 |
| `docs/作业-0923-用户身份识别分析.md` | 作业①：用户标识的识别方式 |
| `docs/作业-0923-文件下载权限分析.md` | 作业②：下载鉴权（含 5 张取证截图） |
| `docs/作业-0923-文件上传逻辑分析.md` | 作业③：上传逻辑与支持类型 |
| `docs/作业-0923-后端接口暴露分析.md` | 作业④：API 是否暴露、浏览器如何访问 |

> ⚠️ `docs/screenshots/` 下另有 4 张 UUID 命名的 DevTools 截图**含未脱敏的会话 ID / 明文口令**，
> 它们**未被跟踪但仍在目录里** —— 不要在该目录执行 `git add -A`。

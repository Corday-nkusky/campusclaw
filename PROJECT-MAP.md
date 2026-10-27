# CampusClaw · 项目地图

> 这份文件回答一个问题：**「我要找的东西在哪？」**
> 面向任何第一次打开这个仓库的人（包括几周后的自己）。
>
> 最后更新：2026-09-23（迭代 1 收尾后）
> ⚠️ 本文件描述**结构与用途**，不描述**完成进度**。进度以
> `openspec/changes/archive/2026-09-23-add-auth-rbac-class-knowledge/tasks.md` 的方括号为唯一真相源。

---

## 0. 三十秒看懂这个仓库

**CampusClaw** 是教学案例：面向中小学的教研智能体。本仓是它的**迭代 1**——教研材料与知识库底座，也就是后续智能能力的地基。

三层关系：

```
规约（契约）  openspec/                     ← 想做对什么，谁说了算
    ↓ 派生
代码（实现）  backend/  frontend/           ← 按规约写出来的东西
    ↓ 证明
证据（验证）  scripts/  docs/               ← 跑出来的结果，不是结论
```

**核心纪律**：规约是契约，代码是派生物。代码偏离规约时，引用 `spec.md` 的 Scenario 驳回，而不是靠追加对话扩大范围。

技术栈：`浏览器 → Nginx → Go → MySQL`，信任边界落在 Go。

「规约 / 代码 / 证据」三层的目录分工（**现在是实然状态，不是目标状态**）：

```
openspec/specs/auth-upload/spec.md        ← Archive 后的长期真相源（已生成）
openspec/changes/archive/2026-09-23-…/    ← 本次迭代的四件套（已归档）
backend/ frontend/                        ← 实现
scripts/ docs/                            ← 证据
deploy/                                   ← 发布形态（Compose 三服务）
```

---

## 1. 顶层目录

```
campusclaw-corday/
├── openspec/       规约（契约）—— 全仓的真相源，代码围着它转
├── backend/        Go 后端 —— 认证 / 授权 / 隔离 / 上传校验全部在这里
├── frontend/       React + Vite 前端 —— 只是界面，不做访问控制
├── apitest/        接口测试（第 5 课交付）—— 一脚本一接口 + 一键跑 + HTML 报告
├── deploy/         Compose 三服务 + Nginx 反代 + 两个 Dockerfile（标准发布形态）
├── scripts/        验证与运维脚本 —— 产出可复核的证据
├── docs/           交付文档、作业答案、取证报告、截图
├── AGENTS.md       harness 纪律与安全红线（给 AI 代理看的约束）
├── README.md       范围 / 不做项 / 单实例 / 跨班返回码 / 启动方式
├── .env.example    配置样例（列全部必填项，无真实值）
├── .env            ⛔ 真实配置，已 gitignore，**永不提交**
├── .gitignore      忽略 .env、node_modules、dist、uploads、临时目录、.verify-logs
└── .dockerignore   构建镜像时排除 .env 与 uploads/
```

各目录的分工与**谁不能碰谁**：

| 目录 | 职责 | 硬规矩 |
|---|---|---|
| `openspec/` | 规约 | `openspec/specs/` 是**长期真相源，只由 Archive 更新**；Apply 期间只改 `changes/` 里的 delta |
| `backend/` | 唯一做访问控制的地方 | 前端传来的 `user_id` / `role` / `class_id` 一律**不可信** |
| `frontend/` | 展示与交互 | **不直连数据库**；隐藏按钮 ≠ 访问控制 |
| `deploy/` | 发布形态 | **只有 `web` 映射宿主端口**；`db` 不映射；`api` 不单独暴露 |
| `scripts/` | 验证 | 脚本输出即验收证据，**必须能脱离本机复现**（命令 + 输出一起留档） |
| `docs/` | 交付物 | 含取证报告，**脱敏后**才入仓 |

---

## 2. `openspec/` —— 规约

```
openspec/
├── project.md                       项目级约定（本仓是什么、边界在哪）
├── specs/                           ⭐ 长期真相源（source of truth）
│   └── auth-upload/spec.md          Archive 生成：R1–R10 + 35 个 Scenario
└── changes/
    ├── archive/
    │   └── 2026-09-23-add-auth-rbac-class-knowledge/
    │       ├── proposal.md           为什么做 + **明确不做什么**（Non-goals）
    │       ├── design.md             怎么做：技术栈决议、D1–D11、四项 ADR
    │       ├── tasks.md              ⭐ **进度唯一真相源**（§1–§9，方括号即状态）
    │       └── specs/auth-upload/
    │           └── spec.md           R1–R10 的 delta（Archive 时并入 specs/）
    └── （活动变更为空 = 本次迭代已归档收尾）
```

### 四件套各管一段

| 文件 | 回答的问题 | 读它的时候看什么 |
|---|---|---|
| `proposal.md` | 为什么做？**不做什么？** | Non-goals。任何越界实现（如平台超级管理员）都在这里被否决 |
| `design.md` | 怎么做？为什么不用别的方案？ | ADR 的**备选与否决理由**。照录教程原文不算 ADR |
| `spec.md` | 做到什么算合格？ | `### Requirement` 是要点，`#### Scenario` 是**可判定的验收条件** |
| `tasks.md` | 做到哪一步了？ | 方括号。**没跑就勾 = 进度失真** |

> 归档后 `changes/` 下**应该是空的**，`specs/auth-upload/spec.md` 才是真相源。
> 如果你在 `changes/` 里又看到活动变更，说明下一轮迭代又开始了，那是正常的。

---

## 3. `backend/` —— Go 后端

```
backend/
├── go.mod / go.sum                  模块定义与依赖（模块路径含 /backend）
├── cmd/server/main.go               入口：读配置 → **先监听** → 后台建池/迁移/种子
├── internal/
│   ├── config/config.go             必填环境变量（DB/种子/密钥等）；缺失即启动失败，**无内置默认值**；
│   │                                另有 ACCESS_TTL_MINUTES / REFRESH_TTL_DAYS（选填，见 .env.example）
│   ├── db/
│   │   ├── db.go                    连接池（OpenPool / WaitReady / Ping）
│   │   ├── health.go                存活探测器：周期 Ping，状态**变化时**才打日志
│   │   ├── schema.go                六表 DDL：classes / users / materials / knowledge_entries /
│   │   │                            knowledge_chunks / refresh_tokens
│   │   └── seed.go                  幂等种子（班级、账号、材料）+ **种子材料落盘**
│   ├── auth/
│   │   ├── password.go              bcrypt 哈希与校验
│   │   ├── token.go                 HS256 JWT 签发与验签（access token；自实现，无第三方库）
│   │   ├── token_test.go            验签的单元测试（算法白名单 / 篡改 / 过期）
│   │   ├── refresh.go               refresh token 生成（crypto/rand 32B → base64url）与 SHA-256 摘要
│   │   ├── store.go                 refresh 落库：插入 / 消费轮换 / 重放检测 / 全量吊销 / 清理过期
│   │   ├── session.go               请求上下文里的「服务端认定的当前身份」（role/class 每次查库）
│   │   ├── limiter.go               登录失败限流（用户名 + IP，**进程内存态**）
│   │   └── handlers.go              /api/login、/api/logout、/api/refresh、/api/me + 鉴权中间件
│   ├── materials/handler.go         材料列表 / 详情 / 下载 / **上传**（全部已实现）
│   └── knowledge/knowledge.go       知识库正文存取（**本迭代只存正文**，不做检索）
├── bin/server.exe                   编译产物（gitignore）
└── uploads/                         种子与上传的文件落在这里（gitignore；**不对 HTTP 暴露**）
```

### 请求进后端之后的顺序

```
http 请求
  → logRequests        访问日志
  → requireDB          DB 未就绪？→ 仅 /api/* 返回 503（/health 不受影响）
  → 鉴权中间件          有 `Authorization: Bearer <access>`？验签通过？→ 无/无效则 401 + WWW-Authenticate
  → 查 users/classes    用 token 里的 user_id 反查 role / class_id（每次都读，不缓存）
  → 业务 handler        列表强制 WHERE class_id = 会话班级

（`POST /api/refresh` 是例外：**不套**鉴权中间件——access 大概率已过期，凭请求体里的
 refresh_token 查 `refresh_tokens` 表换新对；`POST /api/login` 同理不套。）
                       按 ID 取：先取行 → 再在 Go 里比对 class_id
  → 跨班或不存在        一律同形 404（对外不可区分）
```

### 上传（步 5）的判定顺序 —— **顺序本身是安全设计**

```
1. http.MaxBytesReader 包住 body     ← 在读之前就限长，超限 → 413
2. ParseMultipartForm(1MiB)           ← 只是内存阈值，**不是大小上限**
3. 扩展名白名单（先 ToLower）          ← .txt / .md，否则 400
4. 读全文 + utf8.Valid + 非空         ← 否则 400
5. 服务端生成存储名（crypto/rand）      ← 客户端文件名只作展示标题
6. 先写文件，再同事务写两表             ← 任一步失败 → 删文件回滚
7. class_id 只取会话，**忽略表单里的**  ← 越权参数直接不读
```

### 几个「为什么」

- **为什么 access 用 JWT、refresh 才落库**（add-token-auth + add-refresh-token）：
  - access 是**无状态 JWT**，每个业务请求**零查库**、验签即用；代价是服务端**没法单独撤销**
    某一枚 access。所以它的 TTL 压到 15 分钟，把"无法撤销"的窗口缩到最小。
  - refresh 是**不透明随机串 + 落库**，价值就在**可吊销**——登出 = 删掉那一行，
    改密 / 禁用 / 疑似泄露 = 删该用户全部行，**至多 15 分钟**内彻底下线。
  - 旧的"会话存服务端"方案（迭代 1）能让登出立刻 401，但那是**把每个请求都变成一次查库**；
    双令牌让绝大多数请求免查库，只在刷新时查一次。
- **为什么 refresh 要轮换且检测重放**：它寿命 30 天，泄露危害大于 access。每次刷新换新值、
  旧值标记 `replaced_by`；旧值再次出现 = 疑似泄露 → 吊销该用户全部 refresh。
- **为什么前端刷新要串行化**：并发请求各自发起刷新会把"正常轮换"误判成"重放"，
  导致自己被全量吊销。模块级互斥 Promise 让并发的 401 共享同一次刷新结果。
- **为什么限流在内存里**：这是**单实例部署**的原因。多副本时每个副本各有一份计数，限流就不准了。
  （refresh 行在 MySQL 是共享的，所以**未登录态的续期**在多副本下是可用的；不准的只有限流。）
- **为什么上传目录只挂在 api 上**：读文件必须先过鉴权接口；一旦被 Nginx 当静态目录暴露，知道文件名就能绕过全部鉴权。
- **为什么启动时不等数据库**：R8.4 要求 DB 不可用时进程仍存活、`/health` 仍 200。
  原先「等库就绪再监听」的写法会让 `/health` 在 DB 挂掉时**连 TCP 都建不起来（curl 返回 000）**，
  编排系统会判成容器不健康并重启 —— 与设计意图正好相反。现在先监听，建表与种子放后台每 5 秒重试。

---

## 4. `frontend/` —— React 前端

```
frontend/
├── package.json / package-lock.json   Vite 6.4.3 + React 18.3 + TypeScript 5.7 + markdown-it
├── tsconfig.json                       TS 配置
├── vite.config.ts                      ⭐ dev server 配置 + /api 代理到 8080
├── index.html                          SPA 外壳
└── src/
    ├── main.tsx                        React 挂载入口
    ├── App.tsx                         状态机：loading → anonymous / authenticated + 页签 + 命令面板
    ├── api.ts                         ⭐ **唯一交互出口**：所有请求都从这里走（含 XHR 上传）
    ├── styles.css                     原生 CSS：设计系统、深色模式、Markdown 正文、响应式
    ├── lib/markdown.ts                markdown-it，**html:false 是唯一的 XSS 防线**
    ├── components/
    │   ├── Toast.tsx                  轻提示（错误停留 6 秒）
    │   ├── CommandPalette.tsx         ⌘/Ctrl+K 命令面板
    │   ├── UploadDialog.tsx           上传对话框（拖拽 / 进度 / 报错）
    │   ├── TutorChat.tsx              智能体对话区：历史加载、逐段流式渲染、[n] 引用 → 材料弹窗
    │   └── TutorConfig.tsx            教师配置：提示词 / 技能源码 / 智能体（绑定技能与材料）
    └── pages/
        ├── LoginPage.tsx              登录页（统一错误文案）
        ├── HomePage.tsx               首页：欢迎区（进材料库/上传）+ 账号卡 + 折叠的开发者信息
        ├── MaterialsPage.tsx          材料页：列表/网格、搜索、详情、上传入口
        ├── KnowledgePage.tsx          知识检索页：检索 / 问答 + 溯源命中卡
        └── TutorPage.tsx              解题助手页：对话/配置切换 + 会话侧栏 + 流式对话
```

### 三条前端铁律

| 铁律 | 说明 |
|---|---|
| **身份只来自 `GET /api/me`** | 刷新后重新问服务端。`localStorage` 里的 `role` 可被篡改，**不作授权依据** |
| **不发跨域请求** | 全程同源 `/api/*`，开发期由 Vite 代理、生产期由 Nginx 反代。**不要为了本地联调给 Go 加 CORS** —— 那会把开发期的便利固化成生产配置 |
| **上传用 XHR 不用 fetch** | `fetch` 无法报告上传进度，而 R10.1 要求进度条。这是选 XHR 的**唯一**原因 |

`vite.config.ts` 里有一个必须保留的细节：**显式 `host: '127.0.0.1'`**。Vite 默认只绑 IPv6 回环 `[::1]`，那样 `http://127.0.0.1:5173` 直接连不上（实测 curl 返回 `000`）。

⚠️ **`HomePage.tsx` 与验证脚本的两处外部依赖**（2026-09-30 首页改版后）：
`browser-verify.mjs` B16 用 `#recheck-session` 点「刷新登录状态」验「401 → 回登录页」；
`e2e-acceptance.mjs` C1 从 `.kv dd` 里找「只能查看与下载」——学生能力描述必须保留这句原文。
B8 依赖折叠面板里的 `pre.raw` 仍在 DOM（不要求可见）。`App.tsx` 的命令面板按钮刻意用
`.btn-palette` 而不是 `.btn-ghost`，注释已写在两处。

---

## 5. `deploy/` —— 发布形态

```
deploy/
├── compose.yaml        三服务 web / api / db；**只有 web 映射宿主端口**
├── api.Dockerfile      多阶段构建，CGO_ENABLED=0，alpine + 非 root 用户
├── web.Dockerfile      npm ci → vite build → nginx:alpine
├── nginx.conf          静态资源 + 反代 /api /health；client_max_body_size 对齐应用上限
└── README.md           用法、验证清单、**未验证项**
```

`nginx.conf` 里三处「不显眼但会真出问题」的地方：

1. `client_max_body_size` 必须 **≥** 应用侧 `MAX_UPLOAD_BYTES`。Nginx 默认 `1m` 而应用默认 `2MiB`，
   不改这一行，1–2 MiB 的文件在应用看来完全合法却永远传不进来，客户端拿到的是 Nginx 的 HTML 错误页。
2. `X-Forwarded-For` 必须用**赋等** `$remote_addr`，不能用 `$proxy_add_x_forwarded_for`。
   应用侧 `clientIP()` 无条件采信 XFF 第一个值做限流键，追加写法等于让攻击者自带一个头就伪造出任意 IP。
3. `/uploads/` 必须显式堵死（`return 404`）。这是 R5.2 的第二道口子。

---

## 6. `scripts/` —— 验证与运维

| 脚本 | 类型 | 干什么 | 何时用 |
|---|---|---|---|
| `init-db.sh` | 运维 | 建库 + 建应用账号 | 第一次搭环境 |
| `dev-run.sh` | 运维 | 载入 `.env` 并启动后端（本机开发态） | 每次开发 |
| `reset-demo-data.sh` | 运维 | 把演示数据复位到种子态（幂等，**自增值也拨回**；含第 5 课运行时数据） | 跑验收前 |
| `restart-backend.sh` | 运维 | 复位后重启后端，让种子材料切片重建 | 复位之后（**不重启则检索全 0 命中**） |
| `verify-all.sh` | **总控** | ⭐ 一条命令按正确顺序跑完全部验收 | 提交前 / 交付时 |
| `verify-auth.sh` | **验证** | 认证 Scenario 逐条判定（21 项） | 改完认证相关代码 |
| `verify-isolation.sh` | **验证** | 班级隔离逐条判定（25 项，含反向对照） | 改完隔离相关代码 |
| `verify-upload.sh` | **验证** | 上传入库逐条判定（40 项，含负面对照与 BOM 归一化） | 改完上传相关代码 |
| `verify-retrieval.sh` | **验证** | 班级范围检索逐条判定（21 项；需嵌入网关 + 索引就绪） | 改完检索相关代码 |
| `verify-health-decoupled.sh` | **验证** | R8.4：`/health` 不查库（场景 A/B，19 项） | 改完后端启动流程 |
| `verify-compose-config.sh` | **验证** | `deploy/` 配置静态校验（17 项，**不证明跑得起来**） | 改完 Compose |
| `check-docs-freshness.sh` | **门禁** | 文档状态数字与真相源（tasks 方括号 / verify-all 预期之和）对账 | 改完任何状态数字后 |
| `tcp-proxy.mjs` | 工具 | 供上面那个脚本制造「运行中 DB 掉线」 | 被脚本调用 |
| `e2e-acceptance.mjs` | **验收** | 真实 Chrome：主路径 + 失败路径（31 项 + 11 张截图） | 交付留证 |
| `e2e-tutor.mjs` | **验收** | 真实 Chrome：第 5 课解题助手（配置 / 建会话 / 流式 / 引用，16 项） | 交付第 5 课 |
| `browser-verify.mjs` | **验证** | 真实 Chrome + CDP 跑前端（19 项断言 + 截图） | 改完前端 |
| `capture-identity-evidence.mjs` | **取证** | 抓真实报文：请求头里的 Cookie、响应体原文 | 要交「系统运行截图」类作业 |
| `capture-download-auth.mjs` | **取证** | 下载鉴权 8 个场景逐一定格截图 | 同上 |

### 验证脚本与取证脚本的分工

```
verify-*       回答「对不对」  → 断言 + 退出码 0/1
capture-*      回答「到底是什么」→ 真实请求头、响应体、截图
```

交付「系统运行截图」类作业时，**后者更管用** —— 它产出的是可直接引用的证据，而不是结论。

### 四个使用注意

1. 变量名用 **`CC_USER`** 不是 `USERNAME` —— Windows 有同名系统变量，会取错值。
2. `verify-auth.sh` **会连打 5 次错误口令触发限流**（这是 R1.2 要验的行为）。它**自己起一个
   监听 `:8092` 的一次性实例**来跑，跑完即 kill，所以**不会**污染主实例的账号状态，可反复重跑。
3. 退出码**不要经过管道取** —— `cmd | tail` 之后 `$?` 是 `tail` 的退出码，恒为 0。
4. `verify-all.sh` 里各脚本的**顺序不是随意的**：几个脚本会真写数据，而有的脚本要求库处于种子态，
   所以它会在需要的位置插 `reset-demo-data.sh`。

---

## 7. `docs/` —— 交付文档与证据

```
docs/
├── 作业-0923-用户身份识别分析.md      作业①：用户标识的识别方式
├── 作业-0923-文件下载权限分析.md      作业②：下载是否需要权限 + 5 张取证截图
├── 作业-0923-文件上传逻辑分析.md      作业③：上传逻辑 / 支持类型 / 是否支持其它格式
├── 作业-0923-后端接口暴露分析.md      作业④：API 是否暴露 + 浏览器如何访问
├── 作业总览-0923.md                  本次课程 6 道作业的状态总表
├── 验收报告-迭代1.md                 ⭐ 判定结论 + 逐条证据 + **未验证清单**
├── 课程进度与后续路线.md             我们处在课程哪个位置、还要干什么
├── 开发环境与运维手册.md             起停服务、端口、账号、排障速查
├── identity-evidence.json           身份识别取证（**脱敏**）
├── download-auth-report.json        下载鉴权取证（**脱敏**）
├── acceptance-evidence.json         e2e 验收取证（**脱敏**）
└── screenshots/
    ├── 01…05-*.png                  登录页 / 登录失败 / 已登录 / 会话失效 / 深色模式
    ├── 06-材料页-本班列表.png
    ├── 07…11-*.png                  下载鉴权五场景（带取证标注条）
    ├── 12-未登录-直连受保护接口-401.png
    ├── 13…22-*.png                  e2e 验收：工作台 / 列表 / 上传进度 / 上传成功 /
    │                                详情 GFM / 网格 / 命令面板 / 学生无上传入口 / 403、404 语义
    ├── browser-login-report.json    前端验证报告（**脱敏**）
    └── ⚠️ 4 张 UUID 命名的 DevTools 截图（含未脱敏会话 ID / 明文口令，**未跟踪**）
```

> ⛔ **`docs/screenshots/` 里那 4 张 UUID 命名的图不要 `git add -A` 带进去** —— 含真实会话 ID，其中一张还含明文口令。
> 目录下有一个 `.gitignore` 专门排除它们（模式按 UUID 形状写，不逐个列名）。它们仍留在本地供参考，
> 等价证据是已脱敏的三个 evidence JSON。

---

## 8. 常见「找不到」的东西

第一次翻这个仓库时容易误判为「缺东西」，这里一次说清：

| 找不到 | 真相 |
|---|---|
| `docker-compose.yml` | 文件名是 **`deploy/compose.yaml`**，且要用 `-f` 指定；它不是被误删 |
| `Dockerfile` / `nginx.conf` | 都在 **`deploy/`** 下，不在仓库根 |
| `openspec/changes/` 里的活动变更 | `archive/` 下是已归档的历史；**当前活动变更是 `add-refresh-token/`**（双令牌，进行中） |
| `backend/bin/` 里没有文件 | 编译产物，`go build` 后才有 |
| `backend/uploads/` 里只有两个 `seed-*.md` | 这是种子材料。**上传功能已实现**，跑一次上传就会多出 `m-<hex>.md` |
| `.env` 不存在 | 从 `.env.example` 复制并填值。**它永远不进 git** |
| 运行 `verify-all.sh` 生成的日志不在仓库里 | 落在 `.verify-logs/`（gitignore）。要留档就自己复制进 `docs/` |

---

## 9. 想快速上手，按这个顺序读

1. **`README.md`** —— 范围、不做项、单实例、跨班 404、怎么启动
2. **`AGENTS.md`** —— 纪律与安全红线（如果是让 AI 代理动这个仓库，这份必读）
3. **`openspec/changes/archive/…/proposal.md`** —— 为什么做、明确不做什么
4. **`openspec/specs/auth-upload/spec.md`** —— 验收标准（长期真相源）
5. **`openspec/changes/archive/…/tasks.md`** —— 做到哪了（历史进度）
6. **`docs/验收报告-迭代1.md`** —— 判定结论与**未验证清单**
7. **`docs/开发环境与运维手册.md`** —— 怎么把服务跑起来

---

*相关文档：`docs/课程进度与后续路线.md`（课程位置与剩余工作）、`docs/开发环境与运维手册.md`（操作速查）、`../lesson3/第3课-完成情况总结.md`（本轮完成情况）*

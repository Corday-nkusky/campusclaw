# Tasks: add-auth-rbac-class-knowledge

> 进度载体**只有本文件的方括号**。没跑就勾 = 进度失真。
> 纪律：一个 task 一轮 = 实现 → 审查 diff → 运行 verify → 提交；**失败项不得带入下一项**。

> ⚠️ **本 change 在归档时仍有 2 条未勾**（§7.5、§9.4），归档命令本身也报了
> `Warning: 3 incomplete task(s) found`（第 3 条是当时的 §9.5 本身）。
> 归档是**明知而为**的取舍：这两条的阻塞都在环境/第三方，不在代码 ——
> §7.5 需要本机装有 Docker，§9.4 需要别人的仓库与预置账号，本机都不具备。
> 未勾项与被阻塞的原因完整留在本文件里，验收报告（`docs/验收报告-迭代1.md` §8）
> 也单列了「未验证清单」。**不要让归档这个动作本身被读成「全部已验证」。**

## 进度快照（2026-09-23 晚，全套实跑）

- **一条命令复现全部证据**：`bash scripts/verify-all.sh` → **166/166 项断言、退出码 0**。
  明细：`verify-auth` 19、`verify-isolation` 25、`verify-upload` 34、
  `verify-health-decoupled` 19、`verify-compose-config` 17、`browser-verify` 21、`e2e-acceptance` 31。
- **§1–§6、§8 已完成并验证**；**§7 只完成到「配置写完 + 静态校验 17/17」**
  —— 本机**没有安装 Docker**，`up`/`down` 实跑未能验证（见 §7.5 与 §8.5）。
- **§9.4 同伴交叉验证未完成**：需要拿到**别人仓库**与预置账号，本机无法完成。
- **验收报告**：`docs/验收报告-迭代1.md`（判定结论 + 逐条证据 + 未验证清单）。
- 本机环境：Go 1.27.1（`D:\tools\go`）、MySQL 8.0.41（`MySQL80` 服务，3306）、
  前端 Vite 6.4.3（5173）、Node 22.22.2、**无 Docker**。
- 本轮修掉两个真实缺陷（详见验收报告 §9）：`/health` 在 DB 不可达时被
  `WaitReady` 的 ~140 秒退避阻塞住监听；`verify-auth.sh` 的限流测试会毒化自己的前置条件、
  不可重跑。

## 1. 骨架与配置（→ R7）

- [x] 1.1 建 `backend/cmd/server` 与 `backend/internal/{config,db,auth,materials,knowledge}`
- [x] 1.2 建 `frontend/`（Vite + React + TypeScript），预留登录页与材料页
      —— 脚手架与 `src/pages/` 已建：Vite **6.4.3** + React 18.3 + TS 5.7，`npx tsc --noEmit` 干净。
      登录页是独立页面（`LoginPage.tsx`）；材料页原先是 `HomePage.tsx` 里的一块占位面板，
      **§6.1 已把它拆成独立的 `pages/MaterialsPage.tsx`**（与「工作台」平级，由 header 页签切换）。
      本条本身是「建脚手架 + 预留」，按此勾选；材料页成品见 §6.1。
      另：`.gitignore` 已含 `frontend/node_modules/`、`frontend/dist/`。
- [x] 1.3 `internal/config` 只从环境变量读取全部配置项（`SESSION_SECRET`、DB 凭据、上传目录与上限、会话 TTL、预置账号口令、登录失败阈值）
- [x] 1.4 必填项缺失时**启动失败并退出**，不得有内置默认值（→ R7.1）
- [x] 1.5 提交 `.env.example`（列全部必填项、无真实值）、`.gitignore`、`.dockerignore`（排除 `.env` 与 `uploads/`）→ R7.2、R7.3
- [x] 1.6 verify：删除必填环境变量后启动，确认报错退出；对照代码读取的环境变量与 `.env.example` 条目一致
      —— 实测退出码 1 并列出全部缺失项；机器核验 `.env.example` 14 条 ↔ 代码读取 14 个键，
      **零遗漏零多余**（必填 13 项 + `API_PORT` 非必填、有默认值 8080）

## 2. 数据与种子（→ R6）

- [x] 2.1 建 `classes`、`users`、`materials`、`knowledge_entries`、`sessions` 五表
- [x] 2.2 `materials` 与 `knowledge_entries` 的 `class_id` 设 **NOT NULL + 索引**（→ R6.4）
- [x] 2.3 种子班级 A / B；账号 `teacher_a`（教师·A）、`student_a1`（学生·A）、`student_b1`（学生·B）
- [x] 2.4 口令来源为环境变量，以 **bcrypt** 存储（→ R6.3）
- [x] 2.5 种子**幂等**：重复启动不复制用户/材料，也不覆盖教师已上传内容（→ R6.1）
- [x] 2.6 两班各至少一条**标题可区分**的材料，并各关联一条知识库正文（→ R6.2）
- [x] 2.7 verify：连续启动两次比对行数；尝试插入空 `class_id` 应被拒绝
      —— 实测 4 次重启后行数恒为 classes 2 / users 3 / materials 2 / knowledge 2，
      启动日志 5 条「已存在，跳过」；`class_id=NULL` 被拒 **ERROR 1048**；
      引用不存在班级被外键拒 **ERROR 1452**；重复 `stored_name` 被唯一键拒 **ERROR 1062**
      （三项均在事务内执行并回滚，行数复核不变）

## 3. 登录与会话（→ R1、R2）

- [x] 3.1 `POST /api/login`：bcrypt 校验；成功签发会话
- [x] 3.2 登录成功**换发新会话 ID**并作废旧会话（→ R1.3）
- [x] 3.3 Cookie 属性 `HttpOnly` + `SameSite=Lax`；本机 HTTP **不加** `Secure`（→ R1.1）
- [x] 3.4 会话读取时 `role` / `class_id` **每次从用户表读取**，不采信客户端（→ R2.1）
- [x] 3.5 `GET /api/me` 返回角色与班级（→ R2.1）
- [x] 3.6 `POST /api/logout` 删除服务端会话行并清 Cookie（→ R1.4）
- [x] 3.7 鉴权中间件：未登录访问受保护接口返回 **401**，响应体不含标题/正文/磁盘路径（→ R9.2）
- [x] 3.8 失败响应同形：账号不存在 / 口令错误 / 被限流三者状态码、响应体、耗时不可区分；**不用 429**（→ R1.2）
- [x] 3.9 用户不存在时执行一次**等价耗时的哈希比较**（→ R1.2）
- [x] 3.10 用户名 + IP 失败限流；锁定期内即使口令正确也拒绝，响应仍与凭据错误同形（→ R1.2）
- [x] 3.11 verify：对比登录前后 Cookie 是否变化；登录后调 `/api/me`；登出后用旧 Cookie 再请求；三种失败各测一次并比对耗时
      —— `verify-auth.sh` 19/19 通过。会话固定用**强验证**：先取有效会话 S1，
      再带 S1 登录另一账号 → S1 立即 401、S2 归属新账号（`student_b1` / `class_id=2`）。
      三类失败的耗时实测 0.0643s / 0.0614s / 0.0624s，同一量级。

## 4. 班级隔离（→ R5）

- [x] 4.1 列表查询**强制带** `WHERE class_id = <会话班级>`
      —— `internal/materials/handler.go` 的 `List`：班级条件写死在 SQL 首行，
      搜索条件只能在其后**追加**（`AND m.title LIKE … ESCAPE '\'`），不存在先搜全库再筛的写法。
      实测 A 班可见 `[1]`、B 班可见 `[2]`，**两班 ID 集合交集为空**。
- [x] 4.2 按 ID 取行**不在 SQL 提前过滤班级**，改 fetch-then-check（→ D4）
      —— `loadGuarded`：`fetchByID` 的 SQL 里**没有** class 条件，取回后在 Go 里比对
      `row.ClassID != sess.ClassID` 才拒。这样跨班与不存在落到同一个出口，同形 404 才成立。
- [x] 4.3 跨班与资源不存在返回**完全同形 404**（→ R5.2）
      —— 实测三者**逐字节相同** `404|{"error":"not_found"}`：
      跨班详情、不存在 ID（`99999999`）、形态非法的 ID（`abc`）。
      跨班文件 `/api/materials/2/file` 同为 404。响应体不含 B 班标题、不含存储名。
- [x] 4.4 详情与下载响应**不含磁盘存储路径**（→ R5.3）
      —— 详情响应逐字段显式构造，**没有 `stored_name` 这一项**；
      实测响应体不含 `stored_name` / `uploads` / 盘符。另：`GET /uploads/…` 直接 404
      （Go 侧无该路由，`File` 是唯一出口）；`File` 内部仍做 `filepath.Base` 基底名校验。
- [x] 4.5 verify：A 班账号带 Cookie 请求 B 班材料 ID 与文件；用 B 班标题在 A 班列表搜索
      —— `scripts/verify-isolation.sh` **25 项全通过**，含：
      A 班用 `B班-班级公约` 搜索 → `count=0`；**B 班同关键词搜索 → `count=1`**
      （后一条是为排除「搜索被写成恒不匹配」的假通过，单看 A 班为空说明不了问题）。
      真实浏览器取证：`docs/identity-evidence.json` 记录了 materials 请求头
      `Cookie: cc_session=…`（64 位）、`document.cookie` 为空、页面内跨班 fetch 得 404；
      截图 `docs/screenshots/06-材料页-本班列表.png`。
- [x] 4.6 种子材料**落盘**（R5 验收的隐含前置，原 tasks 未列）
      —— 种子原先只写 DB 行、磁盘无文件 → `/file` 必然 404，「下载」这条链根本走不通。
      现 `ensureSeedFile` 幂等写盘（判据「文件存在且大小相符」），即使 DB 行已存在也补写。

## 5. 上传入库（→ R3、R4）

- [x] 5.1 `POST /api/materials`：仅教师可用，学生会话返回 403（→ R3.1）
      —— 中间件链是 `RequireSession(RequireTeacher(Upload))`，角色判定在服务端而不是界面。
      实测学生会话上传返回 `403 {"error":"forbidden"}`（`verify-upload.sh` R3.1/R3.2；`e2e-acceptance.mjs` C5）。
- [x] 5.2 扩展名白名单 `.txt` / `.md`，否则 400（→ R4.2）
      —— `filepath.Ext` 结果**先 `ToLower` 再比对**：`filepath.Ext("UPPER.MD")` 返回原样 `.MD`，
      不做归一化就会把大写扩展名误判为非法。实测 `.exe` → `400 unsupported_type`，
      `.MD` → `201`（大小写归一化生效）。
- [x] 5.3 大小上限走 `MAX_UPLOAD_BYTES`，**超限在读完请求体之前**拒绝（413）（→ R4.3）
      —— 用 `http.MaxBytesReader` 包住 `r.Body` 后**再**调 `ParseMultipartForm`。
      ⚠️ `ParseMultipartForm` 的 `maxMemory` 不是大小上限（它只决定内存/临时文件的切换阈值），
      只靠它实现不了「读之前拒绝」。实测超限 → `413 payload_too_large` 且响应体回显 `limit_bytes`。
- [x] 5.4 内容必须非空合法 UTF-8，否则 400（→ R4.4）
      —— `utf8.Valid` + 长度非零。实测非 UTF-8 → `400 invalid_encoding`；空文件 → `400 empty_content`。
- [x] 5.5 磁盘存储名由**服务端生成**；客户端文件名只作展示标题（→ R4.5）
      —— `newStoredName` 用 `crypto/rand` 生成 `m-<32位hex>.<ext>`，并 `os.OpenFile(..., O_EXCL)` 独占创建。
      实测把客户端文件名构造成 `../../evil.md` 仍得 `201`，落库 `stored_name` 形态为
      `^m-[0-9a-f]{32}\.(md|txt)$`，且 `uploads/` 之外**未产生任何文件**（`evil.md` / `../evil.md` 等逐个检查）。
- [x] 5.6 `materials` 与 `knowledge_entries` **同事务**写入；失败删已写文件（→ R4.6、D5）
      —— `insertMaterialTx` 内两条 INSERT 同事务；事务回滚后 `os.Remove` 刚写的文件。
      实测教师上传后两表行数与目录文件数**同步 +1**（`2/2/2` → `3/3/3`），
      知识库正文与上传内容逐字一致。
- [x] 5.7 忽略表单中的 `class_id`，落库用会话班级（→ R4.7）
      —— 表单里的 `class_id` 只记进日志，不参与 SQL。实测塞入 `class_id=2` 后落库仍是会话班级 `1`。
- [x] 5.8 `GET /api/materials/{id}/file` 走鉴权 API，`/uploads` **不做静态暴露**（→ R5.2）
      —— Go 侧没有 `/uploads` 路由；即使拿**新上传**的 `stored_name` 直接请求
      `GET /uploads/<stored_name>` 也是 `404`。
- [x] 5.9 verify：教师上传后两表各增一行；学生上传 403 且两表与目录不变；三种非法上传各测一次
      —— `scripts/verify-upload.sh` **34 项全通过**。含三处负面对照：
      学生 403 前后 `materials`/`knowledge_entries`/目录文件数逐项比对不变（`2/2/2`）；
      `.exe`/超限/非 UTF-8 三种非法上传各自前后两表与目录均不变；
      穿越文件名后 `uploads/` 之外无文件。
      ⚠️ 脚本里 `sqlq()` 必须带 `--default-character-set=utf8mb4`：本机 MySQL 客户端默认跟随
      系统代码页（CP936/GBK），会把 utf8mb4 转成 GBK 再返回，导致「库里是对的、脚本却报不一致」。

## 6. 前端页面（→ R9、R10）

- [x] 6.1 登录页 + 材料页
      —— 材料页从 `HomePage.tsx` 拆成独立页 `pages/MaterialsPage.tsx`，与「工作台」平级，
      由 header 的 `.app-nav` 页签切换（不是弹层）。`npx tsc --noEmit` 干净、`vite build` 成功
      （54 modules，dist 264.73 kB / gzip 96.26 kB）。
- [x] 6.2 刷新后身份以 `GET /api/me` 为准；`localStorage` 中的 role 不作授权依据（→ R9.1）
      —— 整个前端**没有一处写入** localStorage/sessionStorage。「篡改 localStorage 后刷新」实测：
      写入 `role=student` / `class_id=999` 并刷新，界面仍显示服务端的「教师 / A班」（B19）。
- [x] 6.3 上传入口显示依据 `/api/me` 的角色；绕过界面仍被 403（→ R3.2）
      —— 入口与命令面板里的「上传材料」都以 `identity.role === 'teacher'` 为唯一判据，
      而 `identity` 只来自 `GET /api/me`。实测学生界面**无**上传按钮（C2），
      且绕过界面直接 POST 仍 `403`（C5）。
- [x] 6.4 401 → 清状态回登录页；403 / 404 按服务端语义提示（→ R9.3）
      —— 401 分支实测（B16）：登出后点「重新校验」→ 401 → 自动回登录页。
      403/404 分支用 CDP Fetch 域把响应改写成服务端真实返回的响应体再放行，实测界面提示
      「权限不足：上传只有教师可以操作。」/「资源不存在，或不属于你所在的班级。」（C8、C9、C10），
      **没有把 403 伪装成「请重新登录」**（截图 21 / 22）。
- [x] 6.5 Markdown 用库默认转义策略，不执行材料内脚本（→ R9.4）
      —— `lib/markdown.ts` 用 markdown-it 且显式写死 `html: false`（它本来就是默认值，
      写出来是防止后来者「顺手打开」）。实测上传含 `<script>` 与 `<img onerror>` 的正文后：
      `.markdown-body` 内 `script` 元素数 0、`img` 元素数 0、
      `window.__xss_script` / `window.__xss_img` 均 `undefined`，渲染结果里是转义后的 `&lt;script&gt;`（B8、B9）。
- [x] 6.6 本班范围内搜索（→ R10.2）
      —— 搜索走**服务端查询**（`?q=`），班级条件与搜索条件在 SQL 里是 AND；前端没有任何
      「过滤掉别班材料」的代码。实测搜本班关键词命中、用 B 班标题在 A 班搜 `count=0`，
      且 B 班同关键词能搜到（后一条用于排除「搜索被写成恒不匹配」的假通过）（B14、B15）。
- [x] 6.7 体验项：北大红、深浅色、列表/网格、⌘/Ctrl+K 命令面板、上传进度、toast、GFM（→ R10.1）
      —— 逐项实测而非看代码：北大红品牌色 + 内联 SVG favicon；深浅色切换后 `body` 底色
      `rgb(244,244,246)` → `rgb(22,22,26)`（B20）；列表/网格切换（B13）；
      ⌘/Ctrl+K 打开命令面板且教师多出「上传材料」命令（B16）；
      上传进度条截到非零百分比 `上传中… 13%`（B4）；成功 toast（B5）；
      GFM 的粗体/表格/代码块/链接齐备（B10）。

## 7. Compose（→ R8）

- [x] 7.1 `deploy/` 下 Dockerfile（web / api）、`nginx.conf`、`compose.yaml` 三服务
      —— `deploy/api.Dockerfile`（多阶段，`CGO_ENABLED=0` 静态编译 + alpine + 非 root `USER app`）、
      `deploy/web.Dockerfile`（`npm ci` + `npm run build` + nginx 托管）、
      `deploy/nginx.conf`、`deploy/compose.yaml`、`deploy/README.md`。
      ⚠️ **本机没装 Docker，镜像未 build 过**：`golang:1.27-alpine` 等标签是否存在**未验证**。
- [x] 7.2 **只有 web 映射宿主端口**；`db` 不映射；`api` 不单独暴露（→ R8.3）
      —— `db` 与 `api` 都**刻意不写 `ports`**；只有 `web` 写 `"${WEB_PORT:-8080}:80"`。
      实测（静态解析 compose.yaml）：`db` 无 ports、`api` 无 ports、`web` 有 ports，17 项校验通过。
      ⚠️ 但 `docker compose ps` 的实跑确认**未做**（无 Docker）。
- [x] 7.3 api 用普通 DB 账号，管理员口令不下发
      —— `MYSQL_ROOT_PASSWORD` 只出现在 `db` 服务的 environment；api 侧只有 `DB_USER`/`DB_PASSWORD`
      等 5 个普通账号键。静态校验里有一条专门断言 api 不含 `MYSQL_ROOT_PASSWORD`/`MYSQL_PASSWORD`。
- [x] 7.4 `depends_on` 之外应用侧做 DB 连不上时的重试等待
      —— 两层：`depends_on: condition: service_healthy` 管启动顺序；
      应用侧 `retryBootstrap` 每 5 秒一轮（先 `Ping` 再建表/种子），不依赖 healthcheck。
      实测（验收报告 §6 场景 B）：DB 恢复后 **5 秒内**业务接口自动恢复，无需重启进程。
- [ ] 7.5 verify：干净目录 `up` 可见登录页；`down` 后再 `up`（不加 `-v`）数据仍在（→ R8.1、R8.2）
      —— **未完成（本机无 Docker）**。已完成的是静态部分：`scripts/verify-compose-config.sh`
      **17 项全通过**（YAML 可解析、三服务、R8.3 端口边界、R7.3 口令边界、命名卷齐备、
      nginx 三条硬约束、Dockerfile 卫生、.dockerignore 覆盖）。
      **静态校验只证明「配置写对了」，不证明「跑得起来」** —— 该脚本第 7 节会如实打印这一点，
      不冒充通过。补齐步骤见 `deploy/README.md` 的验证清单。

## 8. 验收（→ R8.4）

- [x] 8.1 `openspec validate add-auth-rbac-class-knowledge --strict` 通过
      —— 实测 CLI **v1.13.1**（`npx @fission-ai/openspec@latest`，包名是
      `@fission-ai/openspec`，npm 上没有裸名 `openspec`；前置 Node ≥ 20.19.0）。
      输出 `Change 'add-auth-rbac-class-knowledge' is valid`，**真实退出码 0**。
      ⚠️ 取退出码时**不能经过管道**：`cmd | tail` 后 `$?` 是 `tail` 的退出码，恒为 0。
      另做**负对照**：故意造一个缺 `specs/` 的 change → 报 `has issues`，并具体
      点明需要 `## ADDED/MODIFIED/REMOVED/RENAMED Requirements` 头 +
      每条 requirement 至少一个 `#### Scenario:` 块 —— 证明该命令确实在检查内容。
      `openspec list` 交叉核验本 change 为 **25/60 tasks**，与方括号计数一致。
      （总数后来变成 61：§4 自行补了一条 `4.6 种子材料落盘` —— 那是实现过程中发现的
      隐性缺陷，不在原 task 清单里。最终 **59/61**，余 §7.5、§9.4 见进度快照。）
      本 change 的 spec 结构：1 个 `## ADDED Requirements` 头、10 条
      `### Requirement`（R1–R10）、**35 个 `#### Scenario:`**（所有 `####` 级标题
      均为 Scenario，无杂项）。
- [x] 8.2 主路径四条留证：登录、上传、列表可见、学生只读
      —— `scripts/e2e-acceptance.mjs`（**31 项断言全通过**）+ 截图：
      `13-教师-工作台.png`（登录）、`15-教师-上传进度.png` + `16-教师-上传成功-toast.png`（上传）、
      `14-教师-材料页-列表视图.png`（列表可见，`命中 1 条 → 命中 2 条`）、
      `20-学生-材料页-无上传入口.png`（学生只读：可看列表/详情/下载 200，无上传入口）。
      上传进度是**真截到非零百分比**的（`上传中… 13%`）：本机回环传输是毫秒级，
      脚本用 CDP 的 `Network.emulateNetworkConditions` 把上行限到 30 KB/s（网络层限速，
      不是改界面），载荷约 300 KB，于是有约 10 秒观察窗口。
- [x] 8.3 失败路径三条留证：未登录、学生上传、跨班访问
      —— 同脚本：未登录 `401 {"error":"unauthorized"}` 且响应体不含 `班级公约`/`uploads`/`stored_name`
      （截图 `12-未登录-直连受保护接口-401.png`）；学生绕过界面 POST → `403 {"error":"forbidden"}`；
      跨班 / 不存在 / 非法 ID 三者响应体**逐字节相同** `404 {"error":"not_found"}`，
      跨班文件接口同为 404。
- [x] 8.4 `/health` 不查库：停掉 DB 后 `/health` 仍 200、业务接口 503（→ R8.4）
      —— `scripts/verify-health-decoupled.sh` **19 项全通过**，两个场景：
      **A** DB 从启动起不可达 → 启动 6 秒后 `/health` 就返回 `200 {"status":"ok"}`，
      `/api/materials`、`/api/me`、`/api/login` 全部 `503 {"error":"db_unavailable"}`，进程持续存活；
      **B** DB 运行中途掉线再恢复 → 掉线时 `/health` 仍 200、`/api/*` 转 503，
      恢复后**5 秒内**自动回到 401（说明已放行到鉴权层），全程进程未退出。
      ⚠️ 场景 B 用 `scripts/tcp-proxy.mjs`（TCP 中转，杀转发器 = 掉线），**不是**真的
      `net stop MySQL80` —— 本机非管理员会话执行 `net stop` 返回**「系统错误 5 / 拒绝访问」**，
      脚本会如实报出而不伪装通过。中转对被测进程是等价（更严格）的连通性中断：
      连连接池里的长连接都会被真掐断（日志里出现过 `driver: bad connection`）。
      要额外取证可在管理员窗口跑 `bash scripts/verify-health-decoupled.sh --stop-service`。
- [x] 8.5 判定结论（通过/不通过 + 证据）写进迭代说明；证据可脱离本机复现（命令与输出一起留档）
      —— `docs/验收报告-迭代1.md`：判定结论 **「本机可验证部分全部通过（166/166 断言，退出码 0）；
      整体有条件通过」**，条件是 R8.1/R8.2 的 Compose 实跑未能验证（本机无 Docker）。
      复现入口是一条命令：`bash scripts/verify-all.sh`（自己按正确顺序复位演示数据，
      每步完整输出落 `.verify-logs/`）；报告里逐条列出「未验证 / 未能验证」清单。

## 9. 收尾

- [x] 9.1 README 写明四项：**范围、不做项、单实例部署、跨班返回码**
      —— 对应 README §1 范围、§2 明确不做、§3 单实例、§4 跨班 404
- [x] 9.2 三项 ADR（会话与租户来源 / 跨班 404 及备选 / 上传事务与清理）+ 建议第四项 `/health` 存活语义
      —— design.md 的 ADR-1…ADR-4，每项均含备选与否决理由
- [x] 9.3 打 tag `v0.1.0-auth-upload`
      —— 实测已打（annotated tag）。tag 说明里写明了范围、验收结论（166/166）
      以及**已知未完成项**（Compose 实跑未验证、§9.4 未做），
      避免 tag 说明比实际状态乐观。核对：`git tag -l -n1`。
      ⚠️ **tag 之后有一个修复提交，tag 没有跟着移动**（有意为之）：
      `39ee72e fix(upload): 剥掉开头 UTF-8 BOM（实测缺陷）`。
      该缺陷是写作业③文档时发现的（见 `docs/验收报告-迭代1.md` §9.3），
      修完后断言从 166 → **172**、`verify-upload` 从 34 → **40**。
      之所以不重打 tag：tag 是迭代边界的记录，事后移动它会抹掉
      「打 tag 那一刻的状态」，而那一刻的状态确实有过一个已修缺陷。
      **要交付「修复后的迭代 1」，用 `39ee72e` 而不是 tag 指向的那个 commit。**
- [ ] 9.4 同伴交叉验证：拿对方仓库与预置账号实测一条跨班 URL；被绕过时区分「规约缺陷」还是「实现缺陷」
      —— **未完成（本机不可办）**。它要求拿到**别人的仓库**与其预置账号口令，
      本机既没有对方代码也没有对方口令，做不了。跳过它会把「跨班隔离经第三方复核」
      这件事说成已验证，那是失真。
      可先做的替代验证（已在 §4 / §8.3 完成）：本仓自己的跨班探测
      （跨班 / 不存在 / 非法 ID 三者 404 逐字节同形），以及
      `docs/课程进度与后续路线.md` §7 记下的「个人学期仓 ≠ 小组项目最终结果」这条边界。
      真正要做 9.4 时，需要对方提供：仓库地址 + 其 `.env` 里的预置账号口令，
      然后跑 `BASE=<对方实例> CC_USER=student_a1 PASSWORD=... bash scripts/verify-auth.sh`
      与一次跨班 URL 手测。
- [x] 9.5 Archive：`openspec/specs/auth-upload/spec.md` 生成，活动变更清零
      —— 实测 `npx @fission-ai/openspec@latest archive add-auth-rbac-class-knowledge -y`
      退出码 0，输出 `Change ... archived as '2026-09-23-add-auth-rbac-class-knowledge'`。
      结果核对：
      · `openspec/specs/auth-upload/spec.md` 已生成 —— **10 条 `### Requirement:`、35 个 `#### Scenario:`**，
        与 change 里的 delta 逐条对应（`Totals: + 10, ~ 0, - 0, → 0`）；
        归档自动生成的 `## Purpose` 是 `TBD`，已补成对 R1–R10 的一段概述。
      · `openspec/changes/` 下只剩 `archive/`，**活动变更清零**。
      · ⚠️ 归档时命令打印了 `Warning: 3 incomplete task(s) found`（§7.5、§9.4，以及当时的 §9.5 本身），
        这是**如实**的：归档不等于「全部已验证」，原因见文件头的说明。

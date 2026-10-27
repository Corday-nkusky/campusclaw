# spec delta: auth-upload

> **编号来源说明**：课件只给出能力名 `auth-upload`，以及它在核对总表中引用过的条款号（R1–R5、R7.3、R9、R10）。**完整的 R1–R10 正文我没有课件原始文件**，因此下面的编号体系与条款措辞属**【我方定义】**，是把课件的 Requirement 列表、Scenario 判定表（`s8-verify.html`）与核对总表（`checklist.html`）反向补齐的结果。凡属课件原文要求的内容，逐条标注【教材原文】。

## ADDED Requirements

### Requirement: R1 账号口令认证

系统 SHALL 提供账号口令登录与登出，账号只有**教师**与**学生**两种角色。【教材原文】

#### Scenario: R1.1 正确凭据登录成功

- **WHEN** 用户提交存在且口令正确的账号
- **THEN** 系统建立服务端会话，并下发 `HttpOnly`、`SameSite=Lax` 的会话 Cookie
- **AND** 响应体只含成功标识，不含口令哈希、会话 ID 之外的任何用户敏感字段

#### Scenario: R1.2 失败响应同形

- **WHEN** 分别提交「账号不存在」「口令错误」「该账号已被限流」三类请求
- **THEN** 三者的状态码、响应体结构与耗时量级**不可区分**
- **AND** 状态码不是 429（429 会暴露账号处于锁定状态）

#### Scenario: R1.3 登录成功换发新会话 ID

- **WHEN** 同一浏览器先获得一个未登录的会话，再完成登录
- **THEN** 登录后的会话 ID 与登录前**不同**，且登录前的会话已作废

#### Scenario: R1.4 登出后旧 Cookie 立即失效

- **WHEN** 登录后调用登出，再用**同一 Cookie** 请求任一受保护接口
- **THEN** 服务端会话行已删除，请求返回 401

### Requirement: R2 服务端会话承载身份、角色与班级

系统 SHALL 在服务端维护会话，且会话可判定 `user_id`、`role`、`class_id` 三项。【教材原文】

#### Scenario: R2.1 `/api/me` 返回角色与班级

- **WHEN** 已登录用户请求 `GET /api/me`
- **THEN** 响应包含该用户的角色与班级
- **AND** 这两项取自服务端用户表，不采信请求中携带的任何值

#### Scenario: R2.2 缺会话属性的后果可判定

- **WHEN** 会话中缺失 `class_id`
- **THEN** 班级隔离相关的全部 Scenario 判为**不可判定**（而非通过）

### Requirement: R3 角色能力边界

系统 SHALL 按角色限制能力：教师可上传、查看、下载；学生只能查看和下载。【教材原文】

#### Scenario: R3.1 学生上传被拒绝

- **WHEN** 学生会话 `POST /api/materials` 提交一份格式合法的文件
- **THEN** 返回 403
- **AND** `materials` 表、`knowledge_entries` 表行数与上传目录**均无变化**

#### Scenario: R3.2 角色判定在服务端

- **WHEN** 学生绕过界面直接调用上传接口
- **THEN** 仍返回 403（前端隐藏入口只降低可发现性，不构成访问控制）

### Requirement: R4 材料上传入库

系统 SHALL 支持教师上传 `.txt` / `.md`，解析正文并与材料记录**同事务**写入。【教材原文】

#### Scenario: R4.1 上传成功双表同增

- **WHEN** 教师上传一份非空 UTF-8 的 `.md`
- **THEN** `materials` 与 `knowledge_entries` **各新增一行**，且新增行的 `class_id` 等于会话所在班级

#### Scenario: R4.2 扩展名白名单

- **WHEN** 上传扩展名不在 `.txt` / `.md` 白名单内的文件
- **THEN** 返回 400，且两表与上传目录均无变化

#### Scenario: R4.3 超限在读完请求体之前拒绝

- **WHEN** 上传超过 `MAX_UPLOAD_BYTES` 的文件
- **THEN** 返回 413，且不把整个请求体读进内存

#### Scenario: R4.4 非法编码被拒绝

- **WHEN** 上传内容非空但不是合法 UTF-8
- **THEN** 返回 400，且两表与上传目录均无变化

#### Scenario: R4.5 客户端文件名不参与路径构造

- **WHEN** 上传文件的原始名形如 `../../evil.md`
- **THEN** 磁盘存储名由服务端生成，`uploads/` 之外**不产生任何文件**

#### Scenario: R4.6 入库失败无残留

- **WHEN** 入库阶段发生失败
- **THEN** 已落盘的文件被删除，两表均无该次上传的记录

#### Scenario: R4.7 表单里的 class_id 被忽略

- **WHEN** 教师上传时在表单中塞入另一班的 `class_id`
- **THEN** 落库的 `class_id` 仍是**会话所在班级**

### Requirement: R5 材料读取与班级隔离

系统 SHALL 只允许用户读取本班材料；跨班访问与资源不存在**返回完全同形的 404**。【教材原文】

#### Scenario: R5.1 列表按会话班级过滤

- **WHEN** A 班用户请求 `GET /api/materials`，且用 B 班材料的标题做搜索
- **THEN** 结果为空；列表数据来自数据库查询，不是硬编码条目

#### Scenario: R5.2 跨班按 ID 访问被拒绝

- **WHEN** A 班用户请求 B 班材料的 `GET /api/materials/{id}` 与 `GET /api/materials/{id}/file`
- **THEN** 返回 404
- **AND** 响应体**不含** B 班材料的标题、正文与磁盘存储路径
- **AND** 该 404 与「ID 不存在」时的响应**完全同形**

#### Scenario: R5.3 详情不泄漏磁盘路径

- **WHEN** 本班用户请求材料详情
- **THEN** 返回标题、班级、时间与知识库正文
- **AND** **不返回**磁盘绝对路径

### Requirement: R6 数据与种子

系统 SHALL 提供幂等的种子初始化，包含可区分的 A / B 两班数据。【教材原文】

#### Scenario: R6.1 种子幂等

- **WHEN** 连续启动两次
- **THEN** 用户数与材料数**不增加**，教师已上传的内容**不被覆盖**

#### Scenario: R6.2 双班标题可区分

- **WHEN** 查看两班材料的标题
- **THEN** 标题可区分（否则跨班访问失败无法被证明）

#### Scenario: R6.3 口令来源与存储

- **WHEN** 查看种子账号的口令字段
- **THEN** 为 bcrypt 哈希；明文口令来自环境变量

#### Scenario: R6.4 租户列约束

- **WHEN** 尝试向 `materials` 插入 `class_id` 为空的记录
- **THEN** 被数据库拒绝（NOT NULL）；`class_id` 上存在索引

### Requirement: R7 配置外置与失败即停

系统 SHALL 只从环境变量读取配置；必填项缺失时启动失败。【教材原文】

#### Scenario: R7.1 缺 `SESSION_SECRET` 启动失败

- **WHEN** 不设置 `SESSION_SECRET` 直接启动
- **THEN** 进程报错退出，**不落到任何内置默认值**

#### Scenario: R7.2 仓库无真实密钥

- **WHEN** 检查入库文件与镜像构建上下文
- **THEN** 无可用真实密钥；`.env.example` 已入库；`.dockerignore` 排除 `.env` 与 `uploads/`

#### Scenario: R7.3 `.env.example` 足以复现

- **WHEN** 第三方只按 `.env.example` 填值
- **THEN** 能启动成功（示例文件列出全部必填项，且不含真实值）

### Requirement: R8 可复现启动与存活判定

系统 SHALL 以 Docker Compose 作为标准启动方式，并提供不查库的存活端点。【教材原文】

#### Scenario: R8.1 从零启动可见登录页

- **WHEN** 在干净目录按 README 执行 `docker compose up`
- **THEN** 打开入口可见登录页；未登录访问 `/health` 返回 200

#### Scenario: R8.2 数据卷可存活

- **WHEN** 执行 `docker compose down` 后再次 `up`（**不加 `-v`**）
- **THEN** 数据仍在

#### Scenario: R8.3 只有 web 映射宿主端口

- **WHEN** 检查端口映射
- **THEN** `db` 未映射宿主端口，`api` 未单独暴露

#### Scenario: R8.4 `/health` 不查库

- **WHEN** DB 不可用但进程存活
- **THEN** `/health` 仍返回 200；业务接口返回 503

### Requirement: R9 Web 应用

系统 SHALL 提供登录页与材料页，身份一律以 `GET /api/me` 为准。【教材原文】

#### Scenario: R9.1 刷新后身份以服务端为准

- **WHEN** 篡改浏览器 `localStorage` 中的角色后刷新页面
- **THEN** 界面与权限仍以 `/api/me` 的返回为准

#### Scenario: R9.2 未登录访问受保护接口返回 401

- **WHEN** 清空 Cookie 后请求受保护接口
- **THEN** 返回 401，且响应体**不含任何材料数据**（标题、正文、路径）

#### Scenario: R9.3 401 引导回登录页

- **WHEN** 前端收到 401
- **THEN** 清空本地状态并跳转登录页；403 / 404 按服务端语义提示，**不改变语义**

#### Scenario: R9.4 材料正文渲染不执行脚本

- **WHEN** 材料正文中包含 `<script>` 或 `onerror` 等载荷
- **THEN** 按 Markdown 库的默认转义策略渲染，**不执行其中脚本**

### Requirement: R10 页面体验增强

系统 SHALL 提供下列体验项，且**不能替代**服务端鉴权与班级隔离。【教材原文】

#### Scenario: R10.1 体验项齐备

- **WHEN** 使用前端
- **THEN** 具备：北大红品牌、深浅色切换、列表/网格视图、本班范围内搜索、⌘/Ctrl+K 命令面板、上传进度、toast 提示、Markdown GFM 渲染

#### Scenario: R10.2 搜索范围限于本班

- **WHEN** 使用前端搜索
- **THEN** 过滤范围限于**已加载/已查询的本班数据**，不构成跨班检索

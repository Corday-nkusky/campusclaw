# spec delta: auth-upload

## MODIFIED Requirements

### Requirement: R1 账号口令认证

系统 SHALL 提供账号口令登录与登出，账号只有**教师**与**学生**两种角色。登录成功后
系统 SHALL 签发 **access token（HS256 JWT）与 refresh token（不透明随机串）各一枚** 并在响应体中返回；
后续业务请求通过 `Authorization: Bearer <access_token>` 头携带。

#### Scenario: R1.1 正确凭据登录成功

- **WHEN** 用户提交存在且口令正确的账号
- **THEN** 响应体含 **`access_token`**（三段式 JWT）、**`refresh_token`**（非 JWT 的随机串）、
  `token_type: "bearer"` 与 `expires_in`（access 的剩余秒数）
- **AND** 响应**不含** `Set-Cookie` 头
- **AND** 响应体不含口令哈希等任何用户敏感字段
- **AND** `refresh_tokens` 表新增一行，其 `token_hash` 为 refresh 的 SHA-256 摘要（非原文）

#### Scenario: R1.2 失败响应同形

- **WHEN** 分别提交「账号不存在」「口令错误」「该账号已被限流」三类请求
- **THEN** 三者的状态码、响应体结构与耗时量级**不可区分**
- **AND** 状态码不是 429（429 会暴露账号处于锁定状态）

#### Scenario: R1.3 篡改的 token 被拒绝

- **WHEN** 对已签发 access token 的 payload 或签名段做任意一字节改动后，携带它请求受保护接口
- **THEN** 返回 401（验签失败）
- **AND** `alg` 不是 `HS256` 的 token（含 `none`）一律 401

#### Scenario: R1.4 登出为客户端删除凭证

> 本场景名沿用 `add-token-auth` 的 R1.4（场景名是稳定锚点，不随内容改名）；
> 但语义已**反转**：登出从"纯客户端删本地凭证"变为"服务端删除 refresh 记录"。

- **WHEN** 登录后调用登出（携带其 refresh token）
- **THEN** 服务端**删除**该 refresh 记录（`refresh_tokens` 相应行消失或置 `revoked_at`），返回 200
- **AND** 该 refresh **再也换不出新的 access**（后续 `/api/refresh` 返回 401）
- **AND** 已签发的 access 在其余下 `exp` 内对服务端**仍然有效**——这是无状态 access 的固有属性，
  窗口**最长为一个 access TTL（默认 15 分钟）**，不再是旧的 24 小时

#### Scenario: R1.5 401 带 Bearer challenge

- **WHEN** 任一受保护接口返回 401
- **THEN** 响应头含 `WWW-Authenticate: Bearer`

### Requirement: R9 Web 应用

系统 SHALL 提供登录页与材料页，身份一律以 `GET /api/me` 为准；access 与 refresh 由前端保存在
`localStorage`，access 在每个 API 请求的 `Authorization` 头携带。前端 SHALL 在 access 失效或临近过期时
**静默刷新**，对用户不产生"掉线"感知。

#### Scenario: R9.1 刷新后身份以服务端为准

- **WHEN** 篡改浏览器 `localStorage` 中的角色后刷新页面
- **THEN** 界面与权限仍以 `/api/me` 的返回为准
- **AND** 前端不解析 access token、不从令牌读取任何权限字段

#### Scenario: R9.2 未登录访问受保护接口返回 401

- **WHEN** 不携带 `Authorization` 头（或 access 无效/过期）请求受保护接口
- **THEN** 返回 401，且响应体**不含任何材料数据**（标题、正文、路径）

#### Scenario: R9.3 401 引导回登录页

- **WHEN** 前端收到 401 **且静默刷新也失败**（refresh 不存在 / 过期 / 已吊销 / 重放）
- **THEN** 清除本地 access、refresh 与身份状态并跳转登录页；403 / 404 按服务端语义提示，**不改变语义**

#### Scenario: R9.4 材料正文渲染不执行脚本

- **WHEN** 材料正文中包含 `<script>` 或 `onerror` 等载荷
- **THEN** 按 Markdown 库的默认转义策略渲染，**不执行其中脚本**

#### Scenario: R9.5 在用不掉线（静默刷新）

- **WHEN** 用户在 access 过期后继续操作（例如打开材料列表）
- **THEN** 前端用 refresh 换得新 access 并**自动重放原请求**，用户**无感**、无需重新登录
- **AND** 刷新成功后本地 access 与 refresh 均已更新（refresh 发生轮换，旧值作废）
- **AND** 同一时刻只发起**一次**刷新（并发请求共享同一刷新结果），不因并发触发重放误判

#### Scenario: R9.6 上传请求不被自动重放

- **WHEN** `POST /api/materials`（上传）因 access 过期返回 401
- **THEN** 前端**不自动重放**该上传请求，而是提示重新登录
- **AND** 不产生同一文件被写入两次的结果

## ADDED Requirements

### Requirement: R11 令牌刷新与吊销

系统 SHALL 提供 refresh 机制：凭有效 refresh token 换发新的 access token，并对 refresh
执行**轮换 + 重放检测**。系统 SHALL 在服务端保存 refresh 状态，从而具备**吊销能力**。

#### Scenario: R11.1 有效 refresh 换发新 access

- **WHEN** 携带未过期、未吊销的 refresh token 请求 `POST /api/refresh`
- **THEN** 返回新的 `access_token` 与**新的 `refresh_token`**
- **AND** 旧 refresh 记录被标记为已轮换（`replaced_by` 指向新记录），**旧值立即失效**
- **AND** 该请求**不要求**携带 `Authorization` 头

#### Scenario: R11.2 轮换后旧 refresh 重放被拒绝并止损

- **WHEN** 使用一个**已被轮换过**的 refresh token 再次请求 `/api/refresh`
- **THEN** 返回 401
- **AND** 该用户**全部** refresh 记录被吊销（疑似泄露，强制重新登录）
- **AND** 该用户的 access 在其余下 `exp` 内仍有效，但**至多 15 分钟**后彻底无法续期

#### Scenario: R11.3 过期或已吊销的 refresh 被拒绝

- **WHEN** 使用已过 `expires_at` 或 `revoked_at` 非空的 refresh token 请求 `/api/refresh`
- **THEN** 返回 401（带 `WWW-Authenticate: Bearer`），响应体不含业务数据

#### Scenario: R11.4 库里不存 refresh 原文

- **WHEN** 直接查询 `refresh_tokens` 表
- **THEN** 只能看到 `token_hash`（SHA-256 摘要），**看不到任何可直接使用的 refresh 原文**

#### Scenario: R11.5 主动吊销用户全部会话

- **WHEN** 运维删除某用户的全部 refresh 记录（改密 / 禁用 / 疑似泄露场景）
- **THEN** 该用户**换不出任何新 access**
- **AND** 其已签发的 access 在各自剩余 `exp` 内仍有效——**最迟 15 分钟内**完全下线
- **AND** 此举**不需要**修改或重启服务进程

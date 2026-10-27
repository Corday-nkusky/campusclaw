# spec delta: auth-upload

> 依据老师演示的参考实现（`Authorization: Bearer <JWT>`）改写认证相关条款。
> R3–R8、R10 的判定逻辑不变，不在本 delta 内。
> 注：归档前主 spec 已按本 delta 同步（sync-specs），归档时 MODIFIED 幂等覆盖。

## MODIFIED Requirements

### Requirement: R1 账号口令认证

系统 SHALL 提供账号口令登录与登出，账号只有**教师**与**学生**两种角色。登录成功后
系统 SHALL 签发 JWT 并在响应体中返回；后续请求通过 `Authorization: Bearer` 头携带。

#### Scenario: R1.1 正确凭据登录成功

- **WHEN** 用户提交存在且口令正确的账号
- **THEN** 响应体含三段式 JWT（`token`）、`token_type: "bearer"` 与 `expires_in`（秒）
- **AND** 响应**不含** `Set-Cookie` 头
- **AND** 响应体不含口令哈希等任何用户敏感字段

#### Scenario: R1.2 失败响应同形

- **WHEN** 分别提交「账号不存在」「口令错误」「该账号已被限流」三类请求
- **THEN** 三者的状态码、响应体结构与耗时量级**不可区分**
- **AND** 状态码不是 429（429 会暴露账号处于锁定状态）

#### Scenario: R1.3 篡改的 token 被拒绝

- **WHEN** 对已签发 token 的 payload 或签名段做任意一字节改动后，携带它请求受保护接口
- **THEN** 返回 401（验签失败）
- **AND** `alg` 不是 `HS256` 的 token（含 `none`）一律 401

#### Scenario: R1.4 登出为客户端删除凭证

- **WHEN** 登录后调用登出
- **THEN** 服务端返回 200，且**不保留**任何可吊销的 token 状态（无状态 JWT 的既定权衡，design D2）
- **AND** 前端清除本地保存的 token；已签发 token 在 `exp` 前对服务端仍然有效

#### Scenario: R1.5 401 带 Bearer challenge

- **WHEN** 任一受保护接口返回 401
- **THEN** 响应头含 `WWW-Authenticate: Bearer`

### Requirement: R2 token 承载身份标识，角色与班级由服务端判定

系统 SHALL 以 JWT 中的 `user_id` 为唯一身份标识；`role` 与 `class_id` SHALL
每次从服务端 `users` / `classes` 表读取，不采信 token 或请求中的任何其他值。

#### Scenario: R2.1 `/api/me` 返回角色与班级

- **WHEN** 携带有效 token 请求 `GET /api/me`
- **THEN** 响应包含该用户的角色与班级
- **AND** 这两项取自服务端用户表，即使 token 被篡改出其他声明也不生效

#### Scenario: R2.2 token 指向的用户不存在

- **WHEN** token 验签通过但 `user_id` 对应的用户已不存在
- **THEN** 返回 401

### Requirement: R9 Web 应用

系统 SHALL 提供登录页与材料页，身份一律以 `GET /api/me` 为准；token 由前端保存在
`localStorage` 并在每个 API 请求的 `Authorization` 头携带。

#### Scenario: R9.1 刷新后身份以服务端为准

- **WHEN** 篡改浏览器 `localStorage` 中的角色后刷新页面
- **THEN** 界面与权限仍以 `/api/me` 的返回为准
- **AND** 前端不解析 token、不从 token 读取任何权限字段

#### Scenario: R9.2 未登录访问受保护接口返回 401

- **WHEN** 不携带 `Authorization` 头（或 token 无效/过期）请求受保护接口
- **THEN** 返回 401，且响应体**不含任何材料数据**（标题、正文、路径）

#### Scenario: R9.3 401 引导回登录页

- **WHEN** 前端收到 401
- **THEN** 清除本地 token 与身份状态并跳转登录页；403 / 404 按服务端语义提示，**不改变语义**

#### Scenario: R9.4 材料正文渲染不执行脚本

- **WHEN** 材料正文中包含 `<script>` 或 `onerror` 等载荷
- **THEN** 按 Markdown 库的默认转义策略渲染，**不执行其中脚本**

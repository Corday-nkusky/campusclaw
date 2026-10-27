# Design: add-token-auth

## D1 token 只承载 `user_id`，角色与班级仍每次查库

**决议**：JWT payload 只有 `user_id`、`iat`、`exp` 三个声明。`role` / `class_id`
**不进 token**，中间件验签后拿 `user_id` 查 `users JOIN classes` 得到。

**理由**：迭代 1 的 design D1/D2 立下的规矩是「改了用户的角色或调班，下一次请求即生效」，
靠的就是身份属性不缓存在客户端。把 `role` 塞进 JWT，教师降级 / 学生调班后旧 token
在 `exp` 前仍带着旧角色 —— 权限撤销从「立即」退化成「最多 TTL」。查库成本是每请求
一条主键 JOIN，可接受。

## D2 无状态 JWT：登出立即失效**做不到**，接受并写明

**决议**：服务端不维护任何 token 状态（无吊销表、无黑名单）。登出 = 前端删
`localStorage` + 服务端回 200。已签发的 token 在 `exp` 前对服务端始终有效。

**理由**：这正是 session 与 JWT 的架构分界点（见会话分析文档 §2.4 的对照表）。
选 JWT 就要接受这条；补黑名单等于退回有状态。**spec R1.4 已按此语义改写**，
验收脚本不再断言「登出后旧凭证 401」。

**缓解**：TTL 由 `SESSION_TTL_HOURS` 控制（默认 24h）；token 一旦泄露，风险窗口
= 剩余有效期。对课程演示场景可接受。

## D3 HS256 自实现，不引第三方库

**决议**：`internal/auth/token.go` 用 `crypto/hmac` + `crypto/sha256` +
`encoding/base64`（RawURLEncoding）+ `encoding/json` 实现签发与验证，约 120 行。

**理由**：① 依赖最小化（课程项目连编排框架都不引）；② HS256 是「HMAC-SHA256 后
base64url」的组合，stdlib 直接覆盖，没有需要造的密码学轮子；③ 验签代码显式可读，
便于课程讲解 JWT 三段结构。

**安全要点**：
- **算法白名单**：解析 header 后必须校验 `alg == "HS256"`，其余（含 `none`）一律拒 ——
  防 `alg: none` / 算法混淆攻击。
- **常数时间比较**：签名比对用 `hmac.Equal`，不用 `==`（防时序侧信道）。
- **签名覆盖完整两段**：`HMAC(secret, header + "." + payload)`，payload 改一位即失效。

## D4 传输与存储

- 请求头：`Authorization: Bearer <token>`，格式不符（缺 Bearer 前缀 / 空 / 多段）→ 401。
- 前端存储：`localStorage["cc_token"]`。
- **改动的 R9.1 语义**：迭代 1 规约写的是「不写 localStorage」，防的是**把本地值当
  授权依据**。JWT 路线下 localStorage 只保存**凭证**，身份判定仍只以 `GET /api/me`
  为准 —— 前端不解析 token payload、不从 token 读角色，篡改 localStorage 中的
  （不存在的）role 依旧无从生效。spec R9.1 场景文本因此无需修改，仅注释更新。

## D5 文件下载改造

**问题**：迭代 1 下载用 `<a href="/api/materials/{id}/file">` 浏览器导航，Cookie 自动
携带。Bearer 头**浏览器导航带不了**，导航请求必然 401。

**决议**：改为 `fetch`（带 Authorization 头）→ `res.blob()` → `URL.createObjectURL`
→ 动态 `<a download>` 触发保存 → `revokeObjectURL`。文件名取自响应头
`Content-Disposition`，解析失败时退回 `材料标题 + 原扩展名`。

## D6 配置：密钥名沿用 `SESSION_SECRET`，语义变化写明

**决议**：不新增配置项。`SESSION_SECRET`（openssl rand -hex 32）从「签发会话标识的
密钥」（迭代 1 实际只用它做占位，会话 ID 是纯随机串）变为 **JWT HMAC 签名密钥**；
`SESSION_TTL_HOURS` 从会话行过期时间变为 **token `exp` 时长**。

**理由**：改名（如 `JWT_SECRET`）要同步 compose.yaml、verify-compose-config.sh、
运维手册、`.env`，纯 churn 无安全收益。R7 的三个场景（缺项启动失败 / 无真实密钥入库 /
example 足以复现）逐字成立，spec delta 不含 R7。

## D7 会话固定（原 R1.3）在 JWT 下消失，替换为篡改检测

原 R1.3 防的是「登录前客户端先拿到/注入一个会话 ID，登录后继续用」。JWT 由服务端
**签名生成**，客户端无法预先构造有效 token，该攻击面不存在。场景替换为：
篡改 payload 或签名的 token 必须 401（验签真的在工作）。

## D8 401 补 `WWW-Authenticate: Bearer`

迭代 1 文档记录过偏差：RFC 9110 要求 401 响应可含 challenge，当时用 Cookie 无
challenge 语义故不带。改用 Bearer 后 challenge 语义成立，401 统一加
`WWW-Authenticate: Bearer` —— 关闭该偏差，且是 spec 新增场景 R1.5。

## D9 兼容性边界

- **无灰度/共存期**：Cookie 分支整体删除，`/api/*` 只认 Bearer。旧前端页面在部署
  新后端后会全部 401 → 由前端同步发版解决（本仓库单体同发，无独立部署的旧客户端）。
- **存量 DB**：`sessions` 表不再创建（schema 移除）；已存在的表留着无害，由使用者
  手动清理。`reset-demo-data.sh` 不再引用该表。

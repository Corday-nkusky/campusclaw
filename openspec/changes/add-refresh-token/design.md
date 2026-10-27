# Design: add-refresh-token

## D0 本轮范围

本轮把认证从「单 access token、24h 固定 TTL、无状态」改为
**「access（15min JWT）+ refresh（30d 不透明串，落库可吊销）」**。
R2 的判定逻辑不变（role/class 仍每次查库），不在本 delta 内。
R3–R8、R10 不受影响。
本 delta 相对主规约当前版本（`add-token-auth` 归档后的 JWT 版）撰写。

---

## D1 access 保持 HS256 JWT、TTL 压到 15 分钟

**决议**：access token 仍是 `add-token-auth` 那套自实现 HS256 JWT（`internal/auth/token.go`），
payload **仍只有** `user_id` / `iat` / `exp`，`role` / `class_id` 继续每次查库。
唯一变化是 TTL：由 24 小时压到 **15 分钟**（配置 `ACCESS_TTL_MINUTES`，默认 15）。

**理由**：
- access 保持"可验签即用、零查库"，业务请求路径的开销不变（R2 的"角色每次现读"仍然成立）。
- 缩短 TTL 是双令牌的意义所在——泄露窗口从 24h 降到 15min。TTL 不缩短，引入 refresh 就没有收益。
- 15 分钟是业界常见值（OAuth2 默认建议 5–30 分钟）。选 15 而非 5，是为了在"泄露窗口"与
  "刷新频率 / 库压力"之间取平衡：纯演示场景 5 分钟也够，但会产生更多 refresh 请求。

**不变的部分**：算法白名单（`alg` 必须为 `HS256`）、`hmac.Equal` 常数时间比较、
签名覆盖完整两段、`exp` 校验——全部沿用，`token_test.go` 的断言不受影响（改的是 TTL 入参值）。

## D2 吊销能力的真实边界：登出后 access 仍有 ≤15 分钟残效

**决议**：登出 = **服务端删除该 refresh 记录**（首次引入可吊销状态）。
但**已签发的 access token 在 `exp` 前仍能通过验签**——这是无状态 access 的固有属性，
本轮**不改变**它。

**理由**：access 是无状态的，服务端验签时不查任何表；要做到"登出后 access 立即失效"，
就必须让每个业务请求都查一次吊销表——那等于放弃 access 的无状态性，
把它退化成"不透明串"，D3 已否决。

**因此准确表述是**（写进 spec，避免含糊）：
- 登出后：**换不出新 access**（refresh 已删），用户在下一次刷新失败时被引导重登。
- 已泄露的旧 access：**最长 15 分钟后自然失效**。
- 需要"立即全设备下线"的场景（改密 / 禁用 / 疑似泄露）：删除该用户**全部** refresh 记录，
  效果是**至多 15 分钟**内该用户彻底无法续期。

**对比旧方案**：旧方案登出后令牌仍有效 **24 小时**；新方案把该窗口压缩到 **15 分钟**，
且新增了"主动吊销"这一能力。这是本轮相对 `add-token-auth` design D2 的**净改进**，
不是"仍然做不到"。

## D3 refresh 用不透明随机串 + 落库，不用 JWT

**决议**：refresh token = `crypto/rand` 取 32 字节 → `base64.RawURLEncoding`。
数据库**只存其 SHA-256 摘要**，不存原文（比对摘要即可，库被读也拿不到可用令牌）。

**理由**：
- refresh 的价值在**可吊销**，而可吊销**必须查库**——那 refresh 用不用 JWT 都无所谓，
  用随机串更简单、更短、且没有"忘记校验 exp/alg"的风险面。
- 存摘要而非原文：与第 3 课课件"数据库保存会话标识的摘要值，而非标识原文"一致（`think.html`）。

**表结构**（`refresh_tokens`，新增）：

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT UNSIGNED NOT NULL AUTO_INCREMENT | 主键 |
| `user_id` | BIGINT UNSIGNED NOT NULL | 外键 → `users(id)`，`ON DELETE CASCADE` |
| `token_hash` | CHAR(64) | refresh 的 SHA-256 十六进制（UNIQUE） |
| `expires_at` | TIMESTAMP NOT NULL | 过期时间（签发时刻 + `REFRESH_TTL_DAYS`） |
| `created_at` | TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP | 签发时间 |
| `replaced_by` | CHAR(64) NULL | 轮换时指向新记录的 `token_hash`；非空即表示"已用过" |
| `revoked_at` | TIMESTAMP NULL | 吊销时间；非空即失效 |

索引：`UNIQUE KEY uk_rt_hash (token_hash)`、`KEY idx_rt_user (user_id)`、`KEY idx_rt_expires (expires_at)`。

**不循环引用外键到自身**：`replaced_by` 存 `token_hash` 字符串而非外键 id，避免自引用约束的
删除顺序问题。

## D4 refresh 轮换 + 重放检测

**决议**：每次 `POST /api/refresh` 成功，**旧 refresh 立即作废**（把它的 `replaced_by`
写为新记录的 `token_hash`），返回**新的 refresh**。旧值再次出现即为**重放**。

**重放处理**：若收到的 refresh 记录 `replaced_by` 非空（已被换过）或 `revoked_at` 非空，
判定为**疑似泄露**——**吊销该用户全部 refresh 记录**，返回 401，要求重新登录。

**理由**：refresh 生命周期长（30 天），一旦泄露危害大于 access。轮换 + 重放检测是
OAuth2 refresh token rotation 的标准做法：正常客户端每次拿新值，攻击者复用旧值时
必然触发"已被换过"，从而暴露并止损。

**代价**：并发请求下可能出现"同一 refresh 被两次同时使用"导致误判重放。
缓解：前端**串行化刷新**（刷新进行中时，后续请求排队等待同一个刷新结果，不各自发起刷新），
见 D7。课程场景并发极低，该缓解足够。

## D5 `/api/refresh` 的入参与鉴权

**决议**：`POST /api/refresh`，请求体 `{"refresh_token": "..."}`。
**不需要 `Authorization` 头**（access 大概率已过期，否则用户不必刷新）。

响应：`{"access_token", "refresh_token", "token_type": "bearer", "expires_in"}`。

失败（refresh 不存在 / 过期 / 已吊销 / 重放）：统一 **401**，响应体 `{"error":"invalid_refresh"}`，
且带 `WWW-Authenticate: Bearer`（与 R1.5 一致）。

**限流**：`/api/refresh` 复用登录限流器按 IP 计数，防暴力猜测 refresh 值
（32 字节随机串空间极大，此限流是纵深防御，非主要防线）。

## D6 配置：新增两个 TTL 项，`SESSION_TTL_HOURS` 降级为兼容回退

**决议**：
- 新增 **`ACCESS_TTL_MINUTES`**（默认 15）——access 有效期。
- 新增 **`REFRESH_TTL_DAYS`**（默认 30）——refresh 有效期。
- **`SESSION_TTL_HOURS` 保留**，但不再是主配置：若 `ACCESS_TTL_MINUTES` 未设置而
  `SESSION_TTL_HOURS` 已设置，则 access TTL 取 `SESSION_TTL_HOURS × 60` 分钟（老 `.env` 仍能启动）。
  两者都缺 → 启动失败（沿用 R7.1 的"缺必填即失败"，但此项给出兼容路径）。

**理由**：`add-token-auth` design D6 已确立"不为改名而 churn"的原则。此次若直接删
`SESSION_TTL_HOURS`，所有既有 `.env`（含 `reset-demo-data.sh` 之外的人工配置、
`verify-compose-config.sh` 的检查项）都要同步改。保留它作回退，改动面最小。

**命名语义澄清**（写入 `.env.example` 注释）：`SESSION_SECRET` 仍是 JWT HMAC 签名密钥
（access 用）；refresh 是随机串、**不签名**，故不额外引入 refresh 专用密钥。

## D7 前端静默刷新：401 重试 + 串行化

**决议**：`api.ts` 的 `request()` 增加刷新逻辑：

1. 请求前若已知 access 临近过期（本地记录 `expires_at`，留 30 秒余量）→ 先刷新。
2. 收到 **401** → 尝试刷新一次 → 用新 access **重放原请求**。刷新失败才抛 401 给上层。
3. **同一时刻只允许一个刷新在飞**：用一个模块级 `Promise` 做互斥，并发的 401 都等它，
   避免 N 个请求各发一次刷新（这既省请求，也避开 D4 的重放误判）。

**重放的安全性**：仅对**幂等读**（GET）与**可安全重试**的请求自动重放；
`POST /api/materials`（上传）不在自动重放范围——它由 XHR 独立发起，401 时直接提示重新登录，
避免"文件被上传两次"。

**存储**：access 与 refresh 均存 `localStorage`（键 `cc_access` / `cc_refresh`），
另存 `cc_access_exp`（毫秒时间戳）。`dropToken()` 一并清除三者。
**沿用 R9.1 的性质**：前端仍**不解析 access payload**、不从令牌读任何权限字段，
身份一律来自 `GET /api/me`；`cc_access_exp` 只是本地过期提示，不是授权依据。

## D8 兼容性与迁移

- **旧 access（24h）**：新后端仍能验签（同一密钥、同一算法、payload 结构未变），
  故升级瞬间已登录用户**不会立刻掉线**，可继续用到其 `exp`；前端下次刷新时自然换新。
- **`refresh_tokens` 表**：新增，`CREATE TABLE IF NOT EXISTS`，不影响既有数据。
- **无灰度期**：单体同发（沿用 `add-token-auth` D9 的判断）。
- **登出的行为变化**：`add-token-auth` spec R1.4 的"不保留任何可吊销状态"被本轮
  **MODIFIED 覆盖**——本 change 后登出**会**删库。这是有意的语义反转，不是冲突。

# Proposal: add-refresh-token

## Why

当前认证是**单个 access token、固定 TTL 24 小时、无状态**（`add-token-auth`，已归档）。
它有两个已记录的缺口，本轮一并解决：

1. **体验缺口（主要动机）**：TTL 固定且不滑动，用户登录满 24 小时后**必然被踢回登录页**，
   与"在用就不掉线"的网站常态不符。演示与日常使用中，这条最直观。
2. **安全缺口**：无状态 JWT 下登出只是前端删本地凭证，服务端**没有任何可吊销状态**。
   需要主动踢人（改密、禁用、疑似泄露）时**做不到**，只能等 24 小时自然过期
   （`add-token-auth` design D2 已把这列为"既定权衡"）。

单靠缩短 TTL 不能同时解决两者：TTL 越短掉线越频繁，越长则泄露窗口越大、吊销越迟。
**access + refresh 双令牌**是同时满足"短泄露窗口 + 不掉线 + 可吊销"的标准解。

## What Changes

- **签发**：`POST /api/login` 成功后返回**两个**令牌：
  - `access_token`——HS256 JWT，**TTL 15 分钟**，payload 仍只有 `user_id` / `iat` / `exp`；
  - `refresh_token`——**不透明随机串**（32 字节 `crypto/rand`，base64url），**TTL 30 天**。
- **刷新**：新增 `POST /api/refresh`——凭有效 `refresh_token` 换发**新的 access token**，
  并**轮换 refresh token**（旧的立即作废，返回新的）。刷新不需要 access token。
- **吊销**：新增 `refresh_tokens` 表（MySQL）。**服务端首次有了可吊销的认证状态**——
  登出即删除该 refresh 记录；改密 / 禁用用户 / 疑似泄露可删除其全部 refresh 记录，
  使该用户**至多 15 分钟后**彻底下线（access 剩余寿命）。
- **登出语义改变**：`POST /api/logout` 从"无状态 200"变为**真的删库**——
  删除该请求所属的 refresh 记录。登出后**新 access 换不出来**；已签发的 access
  在 `exp` 前仍有效（≤15 分钟，见 design D2）。
- **前端**：access 存内存 + `localStorage`，refresh 存 `localStorage`；
  `api.ts` 增加**静默刷新**——access 失效（401）或**临近过期**时自动用 refresh 换新并重放原请求，
  用户无感；refresh 也失效才清凭证回登录页。
- **配置**：新增必填项 `ACCESS_TTL_MINUTES`（默认 15）、`REFRESH_TTL_DAYS`（默认 30）；
  `SESSION_TTL_HOURS` **保留但语义变为 access TTL 的兼容回退**（见 design D6）。

## Non-goals

- **不跑、不改任何验收脚本**（`verify-all.sh` / `verify-auth.sh` / `verify-isolation.sh` /
  `verify-upload.sh` / `e2e-acceptance.mjs` / 各 CDP 脚本）。本轮只产出 change 四件套文档，
  **不动 `backend/` 与 `frontend/` 任何代码**；脚本与断言数的同步属于后续 Apply 阶段的独立任务。
- **不改 `openspec/specs/` 主规约**——按 `project.md` 纪律，主规约只由 Archive 更新。
- 不引入 Redis 或任何新组件：refresh 状态落在既有 MySQL 中。
- 不做"记住我 / 多设备会话管理 UI"；不改变 `openapi` / Swagger（第 5 课范围）。
- 不动第 4、5 课已交付内容与任何归档 change。

## 关键取舍

| 备选 | 否决理由 |
|---|---|
| 只缩短 access TTL（如 2h），不加 refresh | 掉线更频繁，且仍无法吊销——两个缺口都没解决 |
| 滑动续期（单令牌，每次请求重签） | 服务端同样要有状态才能吊销；且每次请求都重签、响应头带新 token，客户端实现更绕 |
| access 也做成不透明串查库 | 每个业务请求都多一次库查询；access 保持 JWT 可验签即用，代价最小 |
| refresh 也做成无状态 JWT | 无法吊销，等于白引入一层——正是本轮要修的缺口 |

## 影响面（capability：`auth-upload`）

- **spec delta**：**MODIFIED** R1、R9（新增 R11「令牌刷新与吊销」）；
  R2–R8、R10 判定逻辑不变，不在本 delta 内。
- **代码（Apply 阶段，本轮不写）**：`backend/internal/auth`（新增 refresh 签发/校验/存储、
  handlers 增 `Refresh`、Logout 改删库）、`backend/internal/db/schema.go`（新增表）、
  `backend/internal/config`（新增两个 TTL 项）、`frontend/src/api.ts` + `App.tsx`（静默刷新）。
- **配置面**：`.env.example` 增两项；`SESSION_TTL_HOURS` 语义调整（见 design D6）。
- **验收脚本**：本轮**不动**；Apply 时须同步 `verify-auth.sh` 断言数与 `verify-all.sh` 预期值。

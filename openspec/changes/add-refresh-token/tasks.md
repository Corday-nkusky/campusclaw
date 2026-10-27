# Tasks: add-refresh-token

> **进度（截至本轮）**：§1 签发、§2 存储与接口、§3 前端静默刷新、§5.2 构建 均已完成。
> 剩余：**§2.7**（`/api/refresh` 限流）、**§4 验收脚本同步**、**§5.1 README**、**§5.3 浏览器取证重跑**。
> 纪律：Apply 期间只动 `changes/` 与代码；`openspec/specs/` 主规约只由 Archive 更新。
> **不跑全量验收脚本**——`verify-all.sh` 等由 Apply 阶段的独立任务处理，
> 且任何脚本断言数的改动必须同步其内部"预期"值（见 memory：预期列是硬判定）。

## 1. 后端 · 令牌签发

- [x] 1.1 `internal/auth/refresh.go`（新）：`NewRefresh()` 生成 32 字节 `crypto/rand` → base64url；
      `HashRefresh(s)` 返回 SHA-256 hex（存库用）
- [x] 1.2 `internal/auth/token.go`：`SignToken` 签名键与结构不变；TTL 由调用方传入
      （不再硬绑 `SESSION_TTL_HOURS`）——本轮**未改** `token.go`：它本就收 `ttl` 入参，
      TTL 硬绑发生在调用方，已在 `config.go` / `handlers.go` 侧改掉
- [x] 1.3 `internal/config/config.go`：增 `AccessTTL` / `RefreshTTL`；
      解析 `ACCESS_TTL_MINUTES`（默认 15）、`REFRESH_TTL_DAYS`（默认 30）；
      `SESSION_TTL_HOURS` 作为 access TTL 的兼容回退（design D6）
- [x] 1.4 `.env.example`：增两项并注明 `SESSION_TTL_HOURS` 已降级为回退

## 2. 后端 · 存储与接口

- [x] 2.1 `internal/db/schema.go`：新增 `refresh_tokens` 表（列/索引见 design D3）；
      对 `users` 的外键 `ON DELETE CASCADE`
- [x] 2.2 `internal/auth/store.go`（新）：`InsertRefresh` / `FindRefresh`（按 hash）/
      `RotateRefresh`（写 `replaced_by` + 插新行，同一事务）/ `RevokeAllForUser` /
      `DeleteRefresh`（登出）/ `PurgeExpired`
      —— 实现为 `ConsumeRefresh` 一个入口 + `RefreshUserID` / `DeleteRefresh` / `RevokeAllForUser` / `PurgeExpired`，
      轮换与插新行在同一 `BeginTx` 内
- [x] 2.3 `internal/auth/handlers.go` `Login`：改为返回 `{access_token, refresh_token,
      token_type, expires_in}`；同时把 refresh 摘要与 `expires_at` 写库
- [x] 2.4 `internal/auth/handlers.go` `Refresh`（新，`POST /api/refresh`）：
      查库校验 → **重放/已吊销 → 吊销该用户全部 refresh + 401** → 轮换 → 返回新对（design D4/D5）
- [x] 2.5 `internal/auth/handlers.go` `Logout`：从"无状态 200"改为**按请求体 refresh 删除该行**
      （幂等：删不到也返回 200）
- [x] 2.6 `cmd/server/main.go`：注册 `POST /api/refresh`（**不套 `RequireSession`**，design D5）；
      并注册 `auth.PurgeExpired` 到启动后台清理
- [ ] 2.7 `internal/auth/limiter.go`：`/api/refresh` 复用按 IP 限流（design D5）
      —— **未做**：现有 `Limiter` 与会话/登录流耦合，直接复用需要先看清其作用域，
      留待脚本同步那一批一起处理（不阻塞功能）

## 3. 前端 · 静默刷新

- [x] 3.1 `api.ts`：token 存取改为 `cc_access` / `cc_refresh` / `cc_access_exp`；
      `login` 存两枚；`logout` 带 refresh 调服务端后清三键；`dropToken` 清三键
- [x] 3.2 `api.ts` `request()`：进出双重刷新（临近过期先刷新；401 刷新后重放一次），
      **模块级互斥 Promise 串行化**（design D7）
- [x] 3.3 `api.ts` `uploadMaterial`（XHR）：401 **不自动重放**，直接提示重登（避免重复上传）；
      仅在已知临近过期时先刷新一次
- [x] 3.4 `App.tsx` / `LoginPage.tsx`：登出调用改为带 refresh；文案与注释同步
- [x] 3.5 `MaterialsPage.tsx`：确认下载（fetch blob）路径在 401 后同样走静默刷新
      —— 在 `downloadMaterialFile` 内加了"请求前预刷新 + 401 后重放一次"（幂等读，可安全重放）

## 4. 验收脚本同步（Apply 阶段执行，本轮不做）

- [ ] 4.1 `verify-auth.sh`：断言改为"登录返回双令牌 / refresh 可换新 access /
      旧 refresh 重放 → 401 且全部吊销 / 登出后 refresh 失效"；断言总数与 `verify-all.sh` 预期值**同步**
- [ ] 4.2 `verify-isolation.sh` / `verify-upload.sh`：获取 access 的方式改走 login，
      不额外改断言逻辑
- [ ] 4.3 `reset-demo-data.sh`：加清 `refresh_tokens`（或随 users 级联）
- [ ] 4.4 `verify-all.sh`：预期断言数同步更新（**改断言数必须同步预期列**）

## 5. 文档与收尾

- [ ] 5.1 `README.md`：认证段改为"双令牌 + 静默刷新 + 可吊销"
- [x] 5.2 `go build` + 前端 `tsc` 通过（另加 `go vet` / `gofmt -l` / `vite build` 一并复核）
- [ ] 5.3 （人工确认）浏览器类取证脚本重跑——**与 `add-token-auth` §4.3 的欠账合并处理**

## 待确认（不阻塞文档，Apply 前须定）

- [ ] 6.1 refresh token 是否也设 `HttpOnly` Cookie 形态？当前设计为前端 `localStorage` 承载
      （与既有 R9 一致）。若要防 XSS，refresh 更适合走 `HttpOnly` Cookie——
      **但那会重新引入 CSRF 面**。本轮**不改**，保持 localStorage，与既有决议一致。

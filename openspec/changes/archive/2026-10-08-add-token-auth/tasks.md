# Tasks: add-token-auth

> ⚠️ **归档时仍有 1 条未勾**（§4.3），归档命令已如实打印 `Warning: 1 incomplete task(s)`。
> §4.3「（待老师确认）浏览器类取证脚本全量重跑」阻塞在人工确认，不在代码——实现已交付并在
> 迭代 2 全套验证中持续运行（所有 verify-*/e2e 均以 Bearer token 跑通，2026-10-08 两轮全绿）。
> 归档动作 = 用户授权的流程欠账补齐（delta 与主 spec 曾漂移，归档前已按 delta 同步主 spec
> 的 R1/R2/R9 为 token 版）。**不要把本归档读成「4.3 已确认」。**
> 另：本 change 的 proposal.md 为早期格式（缺 `## Why` / `## What Changes` 标准段），
> 归档器以 non-blocking 警告提示，内容保留原样不回改。

## 1. 后端

- [x] 1.1 `internal/auth/token.go`：HS256 签发 / 验证（算法白名单 + `hmac.Equal` 常数时间比较 + `exp` 校验）
- [x] 1.2 `internal/auth/handlers.go`：Login 返回 `{token, token_type, expires_in}`；RequireSession 改读 `Authorization: Bearer` 并查库取 role/class；Logout 改无状态 200；401 统一加 `WWW-Authenticate: Bearer`
- [x] 1.3 删除 `internal/auth/session.go` 与 `newSessionID`；`cmd/server/main.go` 去掉 sessions Store / PurgeExpired / COOKIE_SECURE
- [x] 1.4 `internal/db/schema.go` 移除 sessions 表；`internal/config` 注释更新（SESSION_SECRET 语义 = JWT 签名密钥）

## 2. 前端

- [x] 2.1 `api.ts`：token 存取（`cc_token`）、request/XHR 统一加 Authorization 头、login 存 token、logout 清 token
- [x] 2.2 文件下载改 fetch blob + ObjectURL（`downloadMaterialFile`），文件名解析 Content-Disposition
- [x] 2.3 `MaterialsPage.tsx` 下载按钮改造；`LoginPage.tsx` / `App.tsx` / `api.ts` 注释与文案同步

## 3. 验收脚本

- [x] 3.1 `verify-auth.sh` 重写：token 三段 / 无 Set-Cookie / 篡改 401 / alg none 401 / 登出语义 / WWW-Authenticate；断言总数同步
- [x] 3.2 `verify-isolation.sh`、`verify-upload.sh`：Cookie jar → Bearer 头
- [x] 3.3 `reset-demo-data.sh` 去 sessions 引用
- [x] 3.4 `browser-verify.mjs`、`e2e-acceptance.mjs`、`capture-identity-evidence.mjs`：Cookie 断言改 token 断言
- [x] 3.5 `verify-all.sh` 预期断言数同步更新

## 4. 文档与收尾

- [x] 4.1 README、运维手册中 Cookie 会话描述改为 token 登录
- [x] 4.2 `go build` + 前端 `tsc` 构建通过；`verify-all.sh --no-browser` 全绿
- [ ] 4.3 （待老师确认）浏览器类取证脚本全量重跑

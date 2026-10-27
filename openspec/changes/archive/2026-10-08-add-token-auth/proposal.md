# Proposal: add-token-auth

## 为什么改

老师用共享屏幕演示了参考实现（截图：`localhost:8080` 的 `materials` 请求，请求头
`Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.…`），要求把登录方式
从「Cookie 会话」改成**token 登录**：登录签发 JWT，前端每个请求在
`Authorization: Bearer` 头里携带。

## 改什么

- **认证载体**：`HttpOnly` 会话 Cookie + MySQL `sessions` 表 → **无状态 JWT（HS256）**
  放 `Authorization: Bearer` 请求头；`sessions` 表与会话存储代码移除。
- **签发**：`POST /api/login` 成功后响应体返回 `{token, token_type, expires_in}`，不再 `Set-Cookie`。
- **验证**：受保护接口只验签名与 `exp`，`user_id` 之外**不采信 token 内任何声明**；
  `role` / `class_id` 仍每次从 `users` / `classes` 表读取（沿用原 design D1/D2，改角色/调班立即生效）。
- **登出**：变为客户端删除 token + 服务端 200。服务端不再有可吊销状态 —— 这是无状态
  JWT 的**既定权衡**（见 design D2），不是遗漏。
- **前端**：token 存 `localStorage`（键 `cc_token`）；`fetch`/XHR 统一加头；
  文件下载从 `<a href>` 导航改为 **fetch blob + ObjectURL**（浏览器导航带不了自定义头）。
- **401 响应**：补 `WWW-Authenticate: Bearer`（顺带关闭迭代 1 文档里记录的
  RFC 9110 §15.5.2 偏差 —— Bearer 方案本来就该有 challenge）。

## 为什么这样改（关键取舍）

| 备选 | 否决理由 |
|---|---|
| JWT + 查 sessions 表吊销 | 状态又回到服务端，等于两套机制叠加，复杂度最高 |
| JWT + 黑名单（Redis） | 引入新组件，超出本课范围 |
| 不透明 token 放 Authorization 头 | 满足"token 登录"字面要求，但与老师演示的 JWT 形态不符 |

## 影响面（capability：`auth-upload`）

- spec delta：**MODIFIED** R1、R2、R9（R3–R8、R10 不动 —— 班级隔离、上传、配置、
  存活判定的判定逻辑全部不变，只是"会话"一词的来源变了）。
- 代码：`backend/internal/auth`（session.go → token.go，handlers.go）、`cmd/server/main.go`、
  `internal/db/schema.go`；`frontend/src/api.ts`、`App.tsx`、`LoginPage.tsx`、`MaterialsPage.tsx`。
- 脚本：`verify-auth.sh`（重写认证断言）、`verify-isolation.sh` / `verify-upload.sh`
  （Cookie jar → Bearer 头）、`reset-demo-data.sh`（去 sessions 行）、三个 CDP 取证脚本
  （Cookie 断言 → token 断言）、`verify-all.sh`（预期断言数同步）。
- 配置面**不变**：`SESSION_SECRET` 名称沿用（语义变为 JWT 签名密钥，见 design D6），
  `SESSION_TTL_HOURS` 语义变为 token 有效期。R7 场景无需改写。

## 不做什么

- 不动第 4 课活动 change `add-class-scoped-retrieval`（已交作业）。
- 不引入第三方 JWT 库（HS256 用 `crypto/hmac` 十几行可实现，理由见 design D4）。
- 不做刷新令牌（refresh token）机制 —— 课程项目无此需求，token 到期重新登录即可。

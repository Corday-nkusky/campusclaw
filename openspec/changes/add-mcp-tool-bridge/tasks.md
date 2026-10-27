# Tasks: add-mcp-tool-bridge

> **实现状态（2026-10-27）**：第 1–6 组已落地并在真实链路上跑通（MCP 服务器 → 连接 → 工具清单 →
> 技能引用 → 会话内调用 → 审计可查）。与本文的差异：① 演示服务器放在 `mcp-server/main.go`
> （独立 `go.mod`，尚未进 `deploy/` 的 compose）；② 演示工具取名 `formula_hint` + `unit_convert`
> （沿用第 6 课示范服务器）；③ **第 7 组（前端界面）未做**。
> 勾选框保持原样，不按本次实现回填——那份清单记录的是当时的计划，不是历史事实。

> 顺序原则：先能独立验证服务器（1），再接主机侧登记与发现（2–3），再动技能校验（4），
> 最后才是会话链路（5）——链路一旦通了，最后补审计与测试（6–8）。
> 骨架（1.1–1.3）必须最先做完并能单独跑通；**不要一次生成整套业务代码**。

## 1. mcp-server 独立服务

- [ ] 1.1 `mcp-server/cmd/server/main.go`：`POST /mcp` + `GET /health`，单文件、只用标准库 `net/http`
- [ ] 1.2 JSON-RPC 2.0 一帧封装：`jsonrpc`/`id`/`method`/`params`；错误码 `-32600` `-32601` `-32602` `-32000`
- [ ] 1.3 Bearer 校验（令牌来自 `MCP_AUTH_TOKEN`）、请求体 1 MB 上限；无令牌 → 401（R6.2、R6.5）
- [ ] 1.4 `initialize`：回 `protocolVersion: "2024-11-05"` + `capabilities.tools`（R6.3）
- [ ] 1.5 `tools/list`：`socratic_hint`（只给提示不给答案）+ `unit_convert`（纯函数），各带 `inputSchema`
- [ ] 1.6 `tools/call`：两个工具的实现；未知工具 → `-32602`
- [ ] 1.7 `mcp-server/go.mod` + `Dockerfile`，`deploy/docker-compose.yml` 增 `mcp-demo` 服务，令牌走环境变量
- [ ] 1.8 单独验证：`docker compose up mcp-demo` → `/health` 200 → 带 Bearer 列工具 → 调工具出文本

## 2. 数据表与登记接口

- [ ] 2.1 `internal/db/schema.go` 加三张表：`mcp_servers`、`mcp_tools`、`mcp_tool_calls`
      （`mcp_servers` 唯一键 `(class_id, name)`；`mcp_tools` 唯一键 `(server_id, name)`）
- [ ] 2.2 MCP-001 `GET /api/mcp/servers`：只回 `has_auth`/`tool_count`，教师，跨班 404（R1.1、R1.4）
- [ ] 2.3 MCP-002 `POST /api/mcp/servers`：同班同名 400；Base URL 格式校验（R1.2、R1.6）
- [ ] 2.4 MCP-003 `PUT /api/mcp/servers/{id}`：只改 name/base_url，忽略 `auth_token`（R1.5）
- [ ] 2.5 MCP-004 `DELETE /api/mcp/servers/{id}`：删服务器与工具缓存，**保留** `mcp_tool_calls`（R5.3）
- [ ] 2.6 学生调用管理接口一律 403（R1.3）

## 3. 连接与发现

- [ ] 3.1 `internal/mcp` 客户端：`initialize` → `tools/list`，JSON-RPC over HTTP，超时 10 s
- [ ] 3.2 MCP-005 `POST /api/mcp/servers/{id}/connect`：成功写 `mcp_tools`；失败 502 且**不写半截**（R2.1、R2.2）
- [ ] 3.3 MCP-006 `POST /api/mcp/servers/{id}/disconnect`：清 `mcp_tools`（R2.4）
- [ ] 3.4 MCP-007 `GET /api/mcp/servers/{id}/tools`：返回最近一次发现清单
- [ ] 3.5 连接失败不得影响会话主路径（用例：全部服务器失败时学生仍可提问）（R2.5）

## 4. 技能工具引用扩展

- [ ] 4.1 `parseSkillFile`：`工具` 取值域扩为内置项 ∨ `工具名@服务器名`；**保留**内置项语义（R3.6）
- [ ] 4.2 格式校验：工具名小写开头、只含 `[a-z0-9_]`，无连字符；服务器名允许连字符（R3.2）
- [ ] 4.3 保存时四类校验：格式 → 服务器存在（本班）→ 已连接 → 在发现清单内（R3.3–R3.5）
- [ ] 4.4 错误文案面向教师可读，指出**哪一条**不满足（不能只说「参数错误」）
- [ ] 4.5 `internal/docs`：把新规则写进 OpenAPI 注解（官方契约仍写「只能是 knowledge_search」，以任务清单为准）

## 5. 会话链路：调用循环

- [ ] 5.1 `chatRequest` 加 `tools` / `tool_choice`；`ChatMessage` 加 `tool_calls` / `tool_call_id`（omitempty）
- [ ] 5.2 `CompleteStream`：**按 `index` 累积 `tool_calls` 分片**，拼完 `arguments` 再解析（R4.3）
- [ ] 5.3 `CompleteStream`：显式丢弃 `delta.reasoning_content`，并加一条断言把「巧合」变成「约束」
- [ ] 5.4 读取 `choices: []` 的 usage 帧，把 token 用量落进审计记录
- [ ] 5.5 可用工具集 = 本次命中技能的声明 ∧ 服务器已连接 ∧ 在发现清单内
- [ ] 5.6 执行器：逐工具授权（未声明 → `denied`，**不出站**）（R4.4）
- [ ] 5.7 双闸门：轮次上限 4、累计上限 6；超限优雅降级（去掉 `tools` 继续收尾）（R4.6、R4.7）
- [ ] 5.8 失败回传：超时/`-32000` → 错误摘要进 `role:"tool"`，绝不静默丢弃（R4.5）
- [ ] 5.9 组消息：工具返回只进 `role:"tool"`；断言 `system` 与无工具时**逐字相同**（R4.8）

## 6. 可观测性

- [ ] 6.1 每次调用写 `mcp_tool_calls`（五个问题 + `request_digest`，**不存参数原文**）（R5.1）
- [ ] 6.2 MCP-008 `GET /api/mcp/tool-calls`：本班最近 50 条，教师，跨班隔离（R5.2）
- [ ] 6.3 SSE `meta` 加可选 `tool_calls`，**无调用时省去而非空数组**，且先于首个 `delta`（R5.4、R4.2）
- [ ] 6.4 前端：MCP 服务器管理页（登记/连接/断开/工具清单）+ 审计列表页

## 7. 测试与门禁

- [ ] 7.1 单测：`tool_calls` 12 分片累积成 1 次调用（用真实报文形状做夹具）
- [ ] 7.2 单测：四类校验各自拒绝，且错误文案指向具体那一条
- [ ] 7.3 单测：双闸门在第 5 轮 / 第 7 次触发降级，链路仍以 `done` 结束
- [ ] 7.4 单测：`reasoning_content` 被丢弃、正文不含思维链
- [ ] 7.5 `apitest/` 加 MCP 专题：非教师 403、跨班 404、未连接不可绑定
- [ ] 7.6 端到端一条：连接 → 注册含 `unit_convert@campus-tools` 的技能 → 提问 → 审计出现 `ok`
- [ ] 7.7 `verify-all.sh` 断言数与 `run` 调用里的预期值**同步修改**
- [ ] 7.8 密钥扫描：源码、`.env.example`、`deploy/` 中无令牌真值（R6.6）

## 8. 交付证据

- [ ] 8.1 `docs/lesson-06/` 存：mcp-server 单独可验的截图/日志、链路可验日志、断开后 `denied` 的记录
- [ ] 8.2 README 补「第 6 课：MCP 接入」一节与本机运行方式（含 `MCP_AUTH_TOKEN` 两侧一致）
- [ ] 8.3 归档本变更（`openspec/changes/archive/`），specs 并入 `openspec/specs/mcp-tool-bridge/`
- [ ] 8.4 打标签（如 `v0.2.0-mcp`）

# Proposal: add-mcp-tool-bridge

## Why

第 5 课的技能只有一段正文被拼进系统消息，模型自己没有「叫工具」的能力：
`skills.go` 里 `onlyTool = "knowledge_search"` 写死，而检索是**服务端在调模型之前**做完的。
技能文件里的 `工具：knowledge_search` 实际是装饰性字段——写不写，链路都一样。

第 6 课要改的是**决策归属**：把「要不要查、查什么」从服务端词法规则交回模型，
由模型输出工具调用、宿主执行并把结果回传。课件原文（`pages/bridge.html`）：

> 决策归模型，执行与收尾归宿主。

同时第 6 课要求外部工具不再是内置常量，而是可登记、可发现、可鉴权调用的 **MCP 服务器**。

**当前仓库的实际状态**：一行都没有。`chatRequest` 里没有 `tools` 字段，
没有 `role:"tool"` 消息的生产者，没有 `mcp_*` 表，没有审计查询接口。

**2026-10-27 已实测**课程网关支持 function calling（详见 `design.md` D1），
所以这条路的**唯一缺口在我们自己的实现**，不在网关能力。

## What Changes

- 新增独立服务 `mcp-server/`：与主应用**同仓、同一 Compose**，`POST /mcp`（JSON-RPC 2.0）+ `GET /health`，
  至少两个工具，令牌只从环境变量读。
- 新增主机侧 MCP 管理：登记服务器、连接（握手 + `tools/list` 并缓存）、断开（清缓存）、
  查看本班调用审计。
- 扩展技能文件：`工具` 支持 `工具名@服务器名`，保存时做四类校验（格式/存在/可用/在清单内）。
- 扩展会话链路：引入工具调用循环（模型 → 宿主执行 → 回传 → 再调模型），双闸门（轮次上限 + 累计次数上限），
  SSE `meta.tool_calls` 与审计落库。
- `/api/ask`（第 4 课非流式链路）**不接工具**——它的契约是「一次检索 + 一次生成」，改动会破坏既有验收。

## Impact

- 受影响规格：新增 capability `mcp-tool-bridge`；`tutor-assistant` 的 R2（技能文件）与 R5（检索）需增量
  （`工具` 字段的取值域从单一常量扩为「内置项 ∨ 外部引用」）。
- 受影响代码：`internal/skills`（解析与校验）、`internal/conversation`（组消息 + 调用循环）、
  `internal/knowledge`（`chatRequest` 增加 `tools`/`tool_choice`，`CompleteStream` 需累积 `tool_calls` 分片）、
  `internal/db/schema.go`（3 张新表）、`cmd/server/main.go`（注册路由）、`deploy/`（新增服务与令牌）。
- 受影响验收：`verify-all.sh` 断言数与预期值必须同步；`apitest/` 增加 MCP 专题用例。

## Non-goals

- 不做 MCP 的 `resources` / `prompts` 两类能力（本课只要 `tools`）。
- 不做 stdio 传输（只做 Streamable HTTP，与示范一致）。
- 不做指标与分布式追踪（课件 `audit.html` 明写留给第 8 课，本课只做日志层）。
- 不做多租户共享服务器：服务器**按班级**隔离，跨班一律 404。
- 不把 `/api/ask` 改成支持工具。

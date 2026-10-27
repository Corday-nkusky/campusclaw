# Design: add-mcp-tool-bridge

## 引言与约束

本变更只在**个人仓** `campusclaw-corday` 上做；小组仓的同类实现是另一次交付，两者不共享代码。
技术栈沿用既有：Go 标准库 `net/http` + MySQL + Qdrant + React/TS，不引入 Web 框架。
所有外部输入（工具返回、工具说明）都按**数据**处理，不按指令处理。

## D1 网关能力先验证，再设计

**决策**：动手前先用真实 `CHAT_API_KEY` 打 4 次真实请求确认网关支持 function calling。

**实测结果（2026-10-27）**：

| 用例 | 结果 |
|---|---|
| 非流式 + `tools` | 200，`finish_reason: tool_calls`，`message.tool_calls[0].function.arguments` 是完整 JSON 串 |
| 流式 + `tools` | 200，`tool_calls` 分 **12 片**；第 1 片给 `id/type/name`，其后只给 `{"index":0,"function":{"arguments":"<片段>"}}` |
| 非流式无 `tools` | 200，`finish_reason: stop`，`message` 里除 `content` 外还有 **`reasoning_content`** |
| 流式无 `tools` | 200，330 行 SSE：`delta.reasoning_content` 292 帧、`delta.content` 328 帧、`choices: []` 的 usage 帧 1 个、`[DONE]` 1 个 |

**推论（直接决定实现）**：

1. `tool_calls` **必须按 `index` 累积、把 `arguments` 拼完再解析**——不能一片当一个调用。
2. `reasoning_content` 必须**显式丢弃**。现有 `CompleteStream` 恰好因为「空 `content` 就 continue」而丢掉它，
   但这是**巧合**，不是设计。本变更补一条断言把巧合变成约束。
3. `usage` 走独立的 `choices: []` 空帧，现有解析器直接跳过 → **token 用量从未被记录**。
   本变更在累积循环里补一处读取，落地到审计记录（第 8 课的指标才有数据可用）。

## D2 工具说明与工具返回都是攻击面，一律进「数据位」

**决策**：`tools[].function.description` 由远端服务器提供，属于**不可信文本**；
工具返回的 `content` 同样不可信。两者都只允许出现在 `tools` 声明与 `role:"tool"` 消息里，
**绝不拼接进 system**。

**理由**：课件 `security.html` 明写「工具返回与工具说明都会原样进提示词，两者都是攻击面」。
第 5 课只挡住了「材料正文进 system」，本变更要把同一原则扩到工具侧。

**代价**：模型对工具用途的理解只能来自 `description` 字段本身，无法加我方前缀强化（那会篡改服务器声明）。
可接受——因为我们已有硬防线（D7 授权 + 配额）兜底。

## D3 令牌只出现在两处

**决策**：服务器令牌只存 `mcp_servers.auth_token`（服务端库）与出站 `Authorization: Bearer` 头；
所有读接口只回 `has_auth` 布尔。`PUT /api/mcp/servers/{id}` **不含令牌字段**（令牌不可改，要换得删了重登）。

**代价**：令牌轮换必须走「删除 + 重建」，操作变重。**接受**——令牌泄露的代价远高于操作便利。

**每台服务器各自一把，不共用**：共用会让「一台服务器被攻破」变成「所有服务器被攻破」。

## D4 连接是显式动作，发现结果落缓存

**决策**：`POST /api/mcp/servers/{id}/connect` 做三步：`initialize`（`protocolVersion: "2024-11-05"`）→
`tools/list` → 写 `mcp_tools`。取消连接（`disconnect`）清空 `mcp_tools`。

**理由**：课件 `binding.html` 的「配置期从严、运行期从简」——保存技能那一刻要能判断工具存在且可用，
不能在每次对话里现连。

**局限（已知且写进 spec）**：这是 **TOCTOU**。保存时工具在清单内，运行前服务器断开 → 调用失败。
这不是校验遗漏，是不可避免的时间窗；表现为「记 `denied` 并回传模型」，不是静默成功。

## D5 技能文件的 `工具` 字段扩展为「内置项 ∨ 外部引用」

**决策**：`工具` 的取值域从 `{knowledge_search}` 扩为：

- `knowledge_search`（内置，不带服务器名，语义不变：服务端检索）；
- `<工具名>@<服务器名>`：工具名**小写字母开头、只含小写字母/数字/下划线**（无连字符）；
  服务器名允许连字符。

保存时四类校验，**任一不过就 400 并给出可读原因**：格式合法 → 服务器存在（本班）→ 已连接 →
工具在该服务器的 `mcp_tools` 清单内。

**与官方契约的落差**：官方 `POST /api/skills` 描述仍写「工具只能是 knowledge_search」。
任务清单要求扩展 → **以任务清单为准**，并把新规则写进我们自己的 OpenAPI 注解（`internal/docs`）。

## D6 调用循环：模型出场三次，双闸门

**决策**：

```
组消息（system + 历史 + 资料 + 提问 + tools 声明）
 → 模型第 1 次：可能直接答，也可能回 tool_calls
 → 宿主执行（逐工具授权 + 计数）
 → 追加 role:"tool" 消息
 → 模型第 2 次 …（循环）
 → 模型最后一次：finish_reason=stop，正文流式下发
```

两个闸门**都要**，缺一不可：

| 闸门 | 防什么 | 值 |
|---|---|---|
| 轮次上限 | 死循环（模型反复调同一工具） | 最多 4 轮工具往返 |
| 累计次数上限 | 成本失控（一轮里调 20 次） | 单次提问最多 6 次工具调用 |

**超限是优雅降级不是报错**：超限后不再提供工具（后续请求不带 `tools`），把已有结果交给模型收尾，
并在 SSE `meta` 上标 `tool_budget_exhausted: true`。

**失败必须回传模型**：工具调用失败（超时/404/`-32000`）→ 记 `denied` 或 `error`，
把错误摘要作为 `role:"tool"` 的 `content` 回传。**静默丢弃会让模型编造工具结果**——这是课件明写的红线。

## D7 四道防线分软硬

| 防线 | 类型 | 落点 |
|---|---|---|
| 位置（工具返回只进 `role:"tool"`） | 软 | 组消息函数 |
| 句式检查（正文里出现「忽略以上指令」类模式整条丢弃） | 软 | 落库前 |
| 逐工具授权（技能里没声明的工具不执行） | 硬 | 执行器入口，先查技能声明 |
| 配额（D6 双闸门） | 硬 | 执行器计数器 |

**模型可以提议，宿主必须复核**：`tool_calls` 里的 `name` 必须能在「本会话可用工具集」里反查到，
否则记为 `denied` 且**不发出站请求**。

## D8 一条审计记录回答五个问题

`mcp_tool_calls` 表字段对应：调了什么（`tool_ref`）、成了没有（`status` ∈ ok/denied/error）、
谁触发（`user_id`）、哪次对话第几轮（`conversation_id` + `turn_index`）、什么时候（`created_at`）。
另存 `request_digest`（参数摘要，SHA-256 前 16 位，**不存原文**，避免把用户数据复制到审计表）。

查询接口 `GET /api/mcp/tool-calls` 返回本班最近 50 条，只有教师可见。

## D9 SSE：`tool_calls` 只在确有调用时出现

**决策**：`meta` 事件加一个可选字段 `tool_calls`，形如
`[{"ref":"formula_hint@campus-tools","status":"ok","text":"<截断 200 字>"}]`；
**没有调用时整个字段省去，不是空数组**。

**顺序**：调用事件先于正文——用户要在等待时就看到「正在查工具」。

## D10 断开后不可用，但历史不清

**决策**：`disconnect` 清 `mcp_tools`，绑定了该服务器工具的技能**仍能保存/读取**（历史不动），
但**调用一律 `denied`**；`DELETE /api/mcp/servers/{id}` 删服务器与工具缓存，**保留 `mcp_tool_calls` 历史**。

**理由**：审计记录一旦可被删，就等于没有审计。

## 取舍记录

- 不为 MCP 单独引一个 Go 库（`mark3labs/mcp-go` 等）：本课只需要 `initialize` / `tools/list` / `tools/call`
  三个方法、JSON-RPC 2.0 一层封皮，自写约 150 行；引库会把「协议长什么样」藏进依赖里，答辩讲不清。
- `mcp-server/` 与主应用同仓同 Compose，不另开 Git 仓（任务清单硬要求）。
- 工具语义沿用示范的「只给提示不给答案」口径：`formula_hint` 换成我们场景的 `socratic_hint`，
  另保留 `unit_convert` 作为纯函数工具（便于证明「工具不必依赖知识库」）。

# Tasks: add-tutor-assistant

> **本 change 的性质**：第 5 课功能**已实现**，本轮**补规约**并据规约核对实现。
> 因此 §1 是本轮的实际产出（已完成），§2 是「delta ↔ 实现」逐条核对的结果，
> §3 是待补的缺口，§4 是覆盖矩阵，§5~§7 是验证与归档。
>
> **纪律**：Apply 期间只动 `changes/` 与代码；`openspec/specs/` 主规约只由 Archive 更新。
> **改任何验收脚本的断言数，必须同步 `verify-all.sh` 里的预期值**（预期列是硬判定，见 §6.2）。

## 1. 规约四件套（本轮产出）

- [x] 1.1 `proposal.md`：Why（代码先行、规约缺位，致第 5 课**无法用 Scenario 验收**）
      / What Changes（新建 capability `tutor-assistant`，R1–R7）/ Non-goals / 关键取舍 / 影响面
- [x] 1.2 `design.md`：D1 capability 划分 … D16 change 命名，共 16 条决策 + 未决项
- [x] 1.3 `specs/tutor-assistant/spec.md`：**ADDED** R1–R7，
      共 **44 条 Scenario**（R1:5 / R2:12 / R3:5 / R4:8 / R5:12 / R6:4 / R7:6）
- [x] 1.4 `tasks.md`（本文件）

## 2. delta ↔ 实现逐条核对（本轮已核）

实现位置与 delta 的对应：

| delta | 实现 |
|---|---|
| R1 提示词 | `backend/internal/prompt/prompt.go`、`class_prompts` 表 |
| R2 技能文件与 CRUD | `backend/internal/skills/skills.go`、`skills` 表 |
| R3 智能体与绑定 | `backend/internal/agents/agents.go`、`agents` / `agent_skills` / `agent_materials` 三表 |
| R4 会话与消息 | `backend/internal/conversation/conversation.go`、`conversations` / `messages` 表 |
| R5 路由与流式 | 同上 + `backend/internal/knowledge/chatstream.go` |
| R6 文档 | `backend/internal/docs/docs.go`（`/openapi.json` + `/swagger/`） |
| R7 验收与测试 | `scripts/verify-all.sh`、`scripts/e2e-tutor.mjs`、`apitest/` |

核对结论：

- [x] 2.1 路由注册：`cmd/server/main.go` 中本课 11 个接口全部注册，
      且 `docs.ServeOpenAPI` / `docs.ServeSwaggerUI` 注册在业务路由**之前**（design D14 的坑）
- [x] 2.2 常量与课件一致：`askTopK = 4`、`historyWindow = 6`、`histMsgMaxRunes = 500`、
      `maxContentLen = 500`、`titleMaxRunes = 24`
- [x] 2.3 SSE 三类事件 + 失败补 `error`：`conversation.go` `streamAnswer` 确认
- [x] 2.4 `meta.status` 恒 `ok`、落空仍作答、`numStripper` 剥离 `[n]`：确认
- [x] 2.5 跨班 404 同形：智能体 / 会话 / 技能三处确认，响应体统一 `{"error":"资源不存在"}`
- [x] 2.6 绑定每次提问重读（`loadAgent` / `loadCandidateSkills` 每次查库，无快照）：确认
- [x] 2.7 智能体停用后已有会话仍可提问（`loadAgent` 注释明写不校验 enabled）：确认
- [x] 2.8 资料不进系统消息（`buildSystem` 只放提示词 + 技能说明 + 边界句）：确认
- [x] 2.9 提示词空 → `defaultRolePrompt` 回退，不拒绝请求：确认
- [x] 2.10 技能五键解析、长度上限、工具名白名单、班内名唯一：确认
- [x] 2.11 智能体删除（DELETE /api/agents/{id}，R3.6~R3.8）：未被引用 204 级联清关联；被会话引用 409 并指向「停用」；学生 403；他班/不存在 404 同形 —— curl 实测四分支

**结论：delta 描述与实现一致，无需为迁就规约改代码。**

## 3. 待补缺口（本轮已补一部分）

- [x] 3.1 `apitest/` 补用例覆盖 R5.1 / R5.2 / R5.3 / R5.4
      → 新增 `cases/api/conversations/{id}/messages/POST__routing.py`
- [x] 3.2 `apitest/` 补用例覆盖 R3.4 / R3.5
      → 新增 `cases/api/agents/PUT__binding.py`
- [x] 3.3 `apitest/` 补用例覆盖 R6.1 ~ R6.4
      → 新增 `cases/openapi.json/GET.py` 与 `cases/swagger/GET.py`
- [ ] 3.4 `apitest/` 补用例覆盖 R4.6（历史只取最近 6 条）/ R4.7（每条截断 500 字）
      —— 需先有一条**不依赖检索命中**的稳定造法：连问若干轮后读库核对 messages 行数
- [ ] 3.5 R5.6（引用前 4 条）/ R5.7（资料范围过滤）
      —— **依赖索引就绪**（混合检索需 Qdrant + 嵌入网关）。前置不满足时应**明确跳过并说明**，
      不得伪装成通过（沿用 `e2e-lesson5.sh` 的 `[WARN] … 跳过` 约定）
- [ ] 3.6 R5.11（生成失败补 error 事件）/ R5.12（资料不进系统消息）
      —— 前者需**注入网关故障**（改配置指向不可达地址），
      后者需**抓取实际发给网关的请求体**（在 chatstream 侧加临时日志或用 mock 网关）——
      两者都属于「需要动点东西才能验」的项，列入独立任务
- [ ] 3.7 R1.3（提示词按班级隔离）/ R1.4（提示词为空回退默认角色）
      —— 前者需 B 班教师令牌（`student_b1` 是 B 班**学生**，没有 B 班教师账号），
      后者需构造空提示词的智能体并观察系统消息（同样需要抓请求体）——同 3.6 归类
- [ ] 3.8 R2.11（同内容重复提交幂等）/ R2.12（改后与他条同名被拒）
      → **已补**：`cases/api/skills/PUT__idempotency.py`（含「改路径上校验同样生效」与「他班 404 同形」）

### runner 的一处改动

- [x] 3.9 `run_all.py` 的用例发现支持 `METHOD__<专题>.py` 形态：
      专题名只进报告标签，**方法名仍是真实 HTTP 方法**（`POST__routing.py` → 方法 `POST`）。
      改动原因：原先用 `routing.py` 会被推导成方法名 `ROUTING`，不是合法 HTTP 方法。
      向后兼容——现有文件不含 `__`，行为不变。
      同步更新 `apitest/README.md` 的目录与命名约定。

## 4. Scenario 覆盖矩阵（实测逐用例比对）

`apitest/` 12 用例 + `scripts/e2e-tutor.mjs` 16 项浏览器断言。

| Scenario | 覆盖来源 | 状态 |
|---|---|---|
| R1.1 教师读写本班提示词 | `apitest/cases/api/prompt/{GET,PUT}.py` | ✅ |
| R1.2 学生不可写提示词 | `apitest/cases/api/prompt/PUT.py` | ✅ |
| R1.3 提示词按班级隔离 | — | ❌ 缺 |
| R1.4 提示词为空回退默认角色 | — | ❌ 缺 |
| R1.5 超长提示词被拒 | `apitest/cases/api/prompt/PUT.py`（over → 400） | ✅ |
| R2.1 合法技能文件被接受 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.2 缺字段被拒 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.3 重复字段被拒 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.4 非法工具名被拒 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.5 启用值只接受是或否 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.6 空行以下缺正文被拒 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.7 超长技能文件被拒 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.8 班内同名 400 | `apitest/cases/api/skills/POST.py` | ✅ |
| R2.9 他班技能直取 404 | `apitest/cases/api/skills/GET.py` | ✅ |
| R2.10 技能读写权限分离 | `apitest/cases/api/skills/{GET,POST}.py` | ✅ |
| R2.11 同内容重复提交幂等 | `apitest/cases/api/skills/PUT__idempotency.py` | ✅（本轮补） |
| R2.12 改后与他条同名被拒 | `apitest/cases/api/skills/PUT__idempotency.py` | ✅（本轮补） |
| R3.1 教师创建智能体 | `apitest/cases/api/agents/POST.py` | ✅ |
| R3.2 学生不可写智能体 | `apitest/cases/api/agents/POST.py` | ✅ |
| R3.3 他班智能体不可见不可取 | `apitest/cases/api/agents/POST.py`（跨班 → 404） | ✅ |
| R3.4 绑定是关系不是快照 | `apitest/cases/api/agents/PUT__binding.py` | ✅（本轮补） |
| R3.5 智能体停用后已有会话仍可提问 | `apitest/cases/api/agents/PUT__binding.py` | ✅（本轮补） |
| R4.1 新建会话锁定智能体 | `apitest/cases/api/conversations/POST.py` | ✅ |
| R4.2 智能体不存在/未启用 404 | `apitest/cases/api/conversations/POST.py`（cross / dis → 404） | ✅ |
| R4.3 跨班会话按编号直取 404 | `apitest/cases/api/conversations/{id}/GET.py` | ✅ |
| R4.4 会话列表只含自己的 | `apitest/cases/api/conversations/GET.py` | ✅ |
| R4.5 提问长度校验 | `apitest/.../messages/POST.py`（空 → 400） | ⚠️ 部分（仅测空，未测 >500 字） |
| R4.6 历史消息截断 6 条 | — | ❌ 缺 |
| R4.7 历史消息逐条截断 500 字 | — | ❌ 缺 |
| R4.8 回答与引用落库 | `apitest/.../messages/POST.py` | ✅ |
| R5.1 路由至多选一个 | `apitest/cases/api/conversations/{id}/messages/POST__routing.py` | ✅（本轮补） |
| R5.2 路由零命中不选技能 | 同上 + `.../messages/POST.py`（无命中时 skill == ""） | ✅ |
| R5.3 同分取编号较小者 | `apitest/cases/api/conversations/{id}/messages/POST__routing.py` | ✅（本轮补） |
| R5.4 停用的技能不进候选集 | `apitest/cases/api/conversations/{id}/messages/POST__routing.py` | ✅（本轮补） |
| R5.5 检索只在绑定材料内 | `apitest/.../messages/POST.py`（citations 限定绑定材料） | ✅ |
| R5.6 引用前 4 条 | — | ❌ 缺 |
| R5.7 资料范围过滤 | — | ❌ 缺 |
| R5.8 SSE 事件顺序 | `apitest/.../messages/POST.py` | ✅ |
| R5.9 meta 恒 ok 且带引用 | `apitest/.../messages/POST.py` | ✅ |
| R5.10 检索落空仍作答且无编号 | `apitest/.../messages/POST.py` | ✅ |
| R5.11 生成失败补 error 事件 | — | ❌ 缺 |
| R5.12 资料不进系统消息 | — | ❌ 缺 |
| R6.1 Swagger 页面可打开 | `apitest/cases/swagger/GET.py` | ✅（本轮补） |
| R6.2 OpenAPI 描述可获取 | `apitest/cases/openapi.json/GET.py` | ✅（本轮补） |
| R6.3 文档覆盖本课接口 | `apitest/cases/openapi.json/GET.py` | ✅（本轮补） |
| R6.4 文档路由不被遮蔽 | `apitest/cases/{openapi.json,swagger}/GET.py`（未登录断言） | ✅（本轮补） |
| R7.1 一条命令跑完验收 | `scripts/verify-all.sh` | ⚠️ 待实测确认 |
| R7.2 断言数与预期必须同步 | `scripts/verify-all.sh`（mismatch 判定） | ⚠️ 待实测确认 |
| R7.3 接口测试按 URL 组织 | `apitest/cases/` 目录结构 | ✅（结构核对） |
| R7.4 接口测试不止对状态码 | `apitest/cases/.../messages/POST.py`（db 交叉核对） | ✅ |
| R7.5 报告不入库 | `.gitignore` | ✅（配置核对） |
| R7.6 浏览器端到端覆盖主路径 | `scripts/e2e-tutor.mjs`（16 项） | ⚠️ 待实测确认 |

**统计**（本轮补完后）：✅ 已覆盖 **36** 条；❌ 明确缺 **9** 条；⚠️ 部分/待实测确认 6 条。
用例总数由 12 → **17**。

**剩余 9 条缺口的性质**（都**不是**「随手就能补」的）：

| 缺口 | 为什么不能立刻补 |
|---|---|
| R1.3 提示词按班级隔离 | 需 **B 班教师**账号（种子只有 B 班学生 `student_b1`）→ 要加种子数据 |
| R1.4 提示词为空回退默认角色 | 需观察**发给网关的请求体**（系统消息内容）→ 要 mock 网关或加临时日志 |
| R4.6 历史只取最近 6 条 | 需连问多轮并**数上下文条数** → 同 R1.4，要看请求体 |
| R4.7 每条截断 500 字 | 同上 |
| R5.6 引用前 4 条 | **依赖索引就绪**（混合检索需 Qdrant + 嵌入网关）→ 前置不满足须明确跳过 |
| R5.7 资料范围过滤 | 同上 |
| R5.11 生成失败补 error 事件 | 需**注入网关故障**（改配置指向不可达地址） |
| R5.12 资料不进系统消息 | 需**抓取实际发给网关的请求体** |
| R4.5 提问 >500 字被拒 | 需确认超长正文在 JSON 传输层不被截断（补一条即可） |

**结论**：9 条里 5 条（R1.4/R4.6/R4.7/R5.12/R1.3）的瓶颈是
**「看不到发给模型的请求体」**——这指向一个**基础设施缺口**：
当前没有可断言的「请求体观测点」。
R5.6/R5.7 的瓶颈是索引依赖；R5.11 需要故障注入。
这三类都应作为**独立任务**处理，不塞进本轮。

## 5. 同伴交叉验证（课件 apply 页 §3，需第二人）

- [ ] 5.1 会话归属：同伴列会话不见我的；用我的会话编号直读 → 404，与读不存在编号完全一致
- [ ] 5.2 智能体隔离：同伴看不到别班智能体；拿别班编号建会话 → 404；智能体停用后已建会话仍可提问
- [ ] 5.3 班级边界：同伴请求体伪造我的班级编号仍按自己班命中；用只在我班材料里的词提问 → 空结果而非报错
- [ ] 5.4 技能开关：同伴把技能改「启用：否」后再问同一句，回答方式明显不同（不进候选集）
- [ ] 5.5 四项均留存截图/日志片段，写入验证记录

## 6. 验证任务（独立执行，非本轮）

- [ ] 6.1 起 MySQL + 后端 + 前端，执行 `bash scripts/verify-all.sh`，退出码 0，断言数 210 项全对
      （原写 209；2026-10-27 补 compose 的 docker 探测后 compose 17→18，合计 210）
- [ ] 6.2 若补了 apitest 用例或改了任何脚本断言数，**同步 `verify-all.sh` 的预期值**
      （`run` 调用里的第二个参数），并实测确认
- [ ] 6.3 `cd apitest && python run_all.py`，退出码 0，生成 HTML 报告
- [ ] 6.4 跑 `node scripts/e2e-tutor.mjs`（真实 Chrome/CDP），16/16 通过
- [ ] 6.5 跑 `npx @fission-ai/openspec@latest validate --all --strict`，无 error

## 7. 归档（独立确认后执行）

- [ ] 7.1 确认 change 名与课程口径是否一致（当前自拟 `add-tutor-assistant`；不一致则 `git mv` 改名）
- [ ] 7.2 归档：`git mv openspec/changes/add-tutor-assistant openspec/changes/archive/2026-10-21-add-tutor-assistant`
- [ ] 7.3 按 delta 把 R1–R7 并入 `openspec/specs/tutor-assistant/spec.md`
- [ ] 7.4 `validate --all --strict` 复核
- [ ] 7.5 更新 `AGENTS.md` 的「当前状态」段（它现在写的是「当前无活动 change」）
- [ ] 7.6 更新 `README.md` §1.1/§8.1 与 `docs/课程进度与后续路线.md`，
      以及 `scripts/check-docs-freshness.sh` 的真相值
      （**注意**：若断言数变化，须同步该脚本的真值；当前 210 若不变则不动）

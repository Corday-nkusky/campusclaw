// Package conversation 承载「会话 + 流式问答」链路（官方 contract CONV-001~004）。
//
// 这是第 5 课的主体：一次提问在服务端按**固定流程**走完十一步
//
//	取会话 → 取智能体 → 取候选技能 → 选技能 → 记问题 → 检索（限绑定材料，前 4）
//	→ 筛切片 → 推 meta → 组消息 → 逐段生成 → 落库并推 done
//
// 检索**不交给模型临场决定**（课件 route/scope 页：服务端按适用条件词法路由）。
// 回答以 text/event-stream 逐段返回：meta → delta* → done。
//
// 两个容易写错的契约点（课件 answer 页原文）：
//   - 流式回答的 meta 里 status **恒为 "ok"**；这一轮有没有依据，靠 citations 是否为空读出，
//     "no_evidence" 是第 4 课 /api/ask 的口径，不是会话链路的。
//   - 检索落空**不终止链路**：仍按角色与技能作答，引用为空、正文不含编号、也不整段回「未找到」。
package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
	"github.com/Corday-nkusky/campusclaw/backend/internal/mcp"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/Corday-nkusky/campusclaw/backend/internal/prompttpl"
)

// 契约常量（官方 openapi + 第 5 课课件 apply/session 页）。
const (
	minContentLen = 2   // 提问去空白后至少 2 字
	maxContentLen = 500 // 提问至多 500 字
	askTopK       = 4   // 检索取前 4 条

	historyWindow    = 6   // 上下文窗口：末尾 6 条消息
	histMsgMaxRunes  = 500 // 上下文里每条消息正文截断 500 字
	materialMaxRunes = 800 // 送模型的每条资料截断 800 字
	titleMaxRunes    = 24  // 首轮提问前 24 字作会话标题

	defaultTitle = "新的提问"
)

// defaultRolePrompt 是智能体提示词为空时的**默认角色设定（fallback）**。
//
// 课件 session 页只规定「提示词为空时回退到默认角色设定，不因此拒绝请求」，未给出具体文案。
// 此处的措辞为**本项目自拟**（未验证与参考实现逐字一致）。
const defaultRolePrompt = "你是本班课程的解题助手，用简体中文作答。"

// guidanceSkillMarker：技能名称含此词时，触发「不给最终答案」的硬性要求（课件原文）。
const guidanceSkillMarker = "解题引导"

// ── 处理器 ────────────────────────────────────────────────────

// Handlers 汇总会话处理器。
type Handlers struct {
	Pool *sql.DB
	Srch *knowledge.SearchDeps
	Chat *knowledge.ChatClient
	// MCP 为 nil 时整条工具链路关闭（只走第 5 课的知识库检索 + 单轮生成）。
	MCP *mcp.Handlers
	// save 是落库助手消息的函数，默认指向 saveAssistant。
	// 抽成字段是为了让 SSE 线格式可被单测（测试可注入假实现，不依赖真实 DB）。
	save func(ctx context.Context, convID int64, content string, citations []knowledge.Hit, skillName string) (int64, error)
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB, srch *knowledge.SearchDeps, chat *knowledge.ChatClient) *Handlers {
	h := &Handlers{Pool: pool, Srch: srch, Chat: chat}
	h.save = h.saveAssistant
	return h
}

// WithMCP 挂上 MCP 执行器，返回自身便于链式调用。
func (h *Handlers) WithMCP(m *mcp.Handlers) *Handlers {
	h.MCP = m
	return h
}

// conversation 是会话对象（响应形态与官方 OpCONV001Response 一致）。
type conversation struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	ClassID   int64  `json:"class_id"`
	AgentID   int64  `json:"agent_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// message 是消息对象（含 citations 与 skill_name）。
type message struct {
	ID        int64           `json:"id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Citations []knowledge.Hit `json:"citations"`
	SkillName string          `json:"skill_name"`
	CreatedAt string          `json:"created_at"`
}

// agentCfg 是会话所选智能体的运行期配置：角色提示词 + 绑定的材料编号。
type agentCfg struct {
	Prompt      string
	MaterialIDs []int64
}

// skillCand 是参与路由的候选技能。
type skillCand struct {
	ID          int64
	Name        string
	Trigger     string // 适用条件（词表，供切词计分）
	Subject     string // 资料范围（非空时只保留标题含该词的材料）
	Description string
	Tools       string // 工具引用（CSV），形如 knowledge_search,unit_convert@demo-tools
}

// ── CONV-001 列出我的会话 ──────────────────────────────────────

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	// 会话列表按 updated_at 倒序（最近问过的排最前，课件 session 页）。
	rows, err := h.Pool.Query(`
		SELECT id, user_id, class_id, agent_id, title, created_at, updated_at
		FROM conversations WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, sess.UserID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.list_failed", "列出会话失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer rows.Close()

	list := []conversation{}
	for rows.Next() {
		var c conversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.ClassID, &c.AgentID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			obslog.ErrorCtx(r.Context(), "conv.scan_failed", "扫描会话行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		obslog.ErrorCtx(r.Context(), "conv.rows_failed", "遍历会话行失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// ── CONV-002 按智能体新建会话 ──────────────────────────────────

type createConvBody struct {
	AgentID int64 `json:"agent_id"`
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req createConvBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求必须是 JSON"})
		return
	}
	if req.AgentID < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择智能体"})
		return
	}

	// 官方契约：必须指定本班**已启用**的智能体。
	//   跨班 / 未启用 / 不存在 → 404（与「不存在」完全同形）；
	//   仅当"本班一个已启用智能体都没有"时 → 400。
	// 判定顺序很重要：先查本班有没有启用的 agent（400 分支），再查目标 agent 是否属于本班且启用（404）。
	var enabledCount int
	if err := h.Pool.QueryRow(
		`SELECT COUNT(*) FROM agents WHERE class_id = ? AND enabled = 1`, sess.ClassID,
	).Scan(&enabledCount); err != nil {
		obslog.ErrorCtx(r.Context(), "conv.agent_list_failed", "查询本班启用智能体失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if enabledCount == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "本班没有已启用的智能体"})
		return
	}

	var x int64
	err := h.Pool.QueryRow(
		`SELECT id FROM agents WHERE id = ? AND class_id = ? AND enabled = 1`,
		req.AgentID, sess.ClassID,
	).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.agent_query_failed", "查询智能体失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	res, err := h.Pool.Exec(
		`INSERT INTO conversations (user_id, class_id, agent_id, title) VALUES (?, ?, ?, ?)`,
		sess.UserID, sess.ClassID, req.AgentID, defaultTitle,
	)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.create_failed", "新建会话失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	id, _ := res.LastInsertId()
	c, err := h.fetchConv(r.Context(), id, sess.UserID, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.readback_failed", "回读新建会话失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// ── CONV-003 读取会话和消息 ────────────────────────────────────

func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求无效"})
		return
	}

	c, err := h.fetchConv(r.Context(), id, sess.UserID, sess.ClassID)
	if errors.Is(err, sql.ErrNoRows) {
		// 别人的会话与不存在同形 404
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.get_failed", "读取会话失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	msgs, err := h.loadMessages(r.Context(), c.ID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.messages_failed", "读取消息失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": msgs})
}

// fetchConv 按 id + user + class 三条件取会话（fetch-then-check，隔离只认会话身份）。
func (h *Handlers) fetchConv(ctx context.Context, id, userID, classID int64) (conversation, error) {
	var c conversation
	err := h.Pool.QueryRowContext(ctx, `
		SELECT id, user_id, class_id, agent_id, title, created_at, updated_at
		FROM conversations WHERE id = ? AND user_id = ? AND class_id = ?`,
		id, userID, classID,
	).Scan(&c.ID, &c.UserID, &c.ClassID, &c.AgentID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (h *Handlers) loadMessages(ctx context.Context, convID int64) ([]message, error) {
	rows, err := h.Pool.QueryContext(ctx, `
		SELECT id, role, content, citations, skill_name, created_at
		FROM messages WHERE conv_id = ? ORDER BY id ASC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []message{}
	for rows.Next() {
		var m message
		var citJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &citJSON, &m.SkillName, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Citations = decodeCitations(citJSON)
		list = append(list, m)
	}
	return list, rows.Err()
}

// loadRecentMessages 取某会话**末尾** limit 条消息（按 id 升序返回），供上下文窗口。
//
// 课件 session 页：窗口固定 6 条，每次回答重新查询，取列表末尾若干条（滑动窗口）。
func (h *Handlers) loadRecentMessages(ctx context.Context, convID int64, limit int) ([]message, error) {
	rows, err := h.Pool.QueryContext(ctx, `
		SELECT id, role, content
		FROM messages WHERE conv_id = ? ORDER BY id DESC LIMIT ?`, convID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rev := []message{}
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		rev = append(rev, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 反转为升序
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

func decodeCitations(ns sql.NullString) []knowledge.Hit {
	out := []knowledge.Hit{}
	if !ns.Valid || ns.String == "" {
		return out
	}
	_ = json.Unmarshal([]byte(ns.String), &out)
	return out
}

// ── CONV-004 在会话中继续提问（SSE）────────────────────────────

type askBody struct {
	Content string `json:"content"`
	AgentID any    `json:"agent_id"` // 收到即忽略（官方契约：正文里的 agent_id 不参与选择）
}

func (h *Handlers) Ask(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求无效"})
		return
	}

	var req askBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求必须是 JSON"})
		return
	}
	content := strings.TrimSpace(req.Content)
	if n := utf8.RuneCountInString(content); n < minContentLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "问题至少需要 2 个字符"})
		return
	} else if n > maxContentLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "问题过长"})
		return
	}
	if req.AgentID != nil {
		obslog.InfoCtx(r.Context(), "conv.agent_id_ignored", "忽略会话提问正文里的 agent_id，改用路径参数", "agent_id", req.AgentID, "user", sess.Username)
	}

	// ── 流开始前：这些错误走正常 HTTP 状态码 ──
	// 步骤 1：取会话（不存在 / 他人 / 他班 → 404）
	conv, err := h.fetchConv(r.Context(), id, sess.UserID, sess.ClassID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if h.Chat == nil || h.Srch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "对话组件未接上"})
		return
	}

	// 步骤 2：取智能体（角色提示词 + 绑定的材料编号）
	ag, err := h.loadAgent(r.Context(), conv.AgentID, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.agent_load_failed", "读取智能体失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	// 步骤 3：取候选技能（该智能体绑定且已启用）
	cands, err := h.loadCandidateSkills(r.Context(), conv.AgentID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.skills_load_failed", "读取候选技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	// 步骤 4：词法路由，至多一个
	skill, hasSkill := routeSkill(cands, content)

	// 步骤 5：记问题（首轮改标题；提问先落库，保证下一轮历史含本轮）
	firstTurn := conv.Title == defaultTitle
	if _, err := h.Pool.ExecContext(r.Context(),
		`INSERT INTO messages (conv_id, role, content) VALUES (?, 'user', ?)`, conv.ID, content); err != nil {
		obslog.ErrorCtx(r.Context(), "conv.user_msg_write_failed", "写入用户消息失败", "conv_id", conv.ID, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if firstTurn {
		if t := titleFrom(content); t != "" {
			_, _ = h.Pool.ExecContext(r.Context(), `UPDATE conversations SET title = ? WHERE id = ?`, t, conv.ID)
		}
	}

	// 步骤 6：检索——只在绑定的材料内，取前 4
	hits, err := h.Srch.HybridPathIn(r.Context(), sess.ClassID, content, askTopK, ag.MaterialIDs)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.retrieval_failed", "会话检索失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务暂时不可用，请稍后重试"})
		return
	}
	// 步骤 7：筛切片——去掉空正文与学科不符，再依次编号
	hits = filterHits(hits, skill, hasSkill)

	// 步骤 9 的输入：上下文窗口（末尾 6 条，含本轮提问）
	history, err := h.loadRecentMessages(r.Context(), conv.ID, historyWindow)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.history_failed", "读取历史消息失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	// ── 开始 SSE：此后错误只能写 event: error，HTTP 保持 200 ──
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stream_unsupported"})
		return
	}

	system := buildSystem(ag.Prompt, skill, hasSkill, len(hits) > 0)
	skillName := ""
	if hasSkill {
		skillName = skill.Name
	}

	// ── 外部工具：只有命中技能且技能里写了 `工具名@服务器名` 才启用（第 6 课）──
	toolSpecs, toolIndex, toolBad := h.prepareTools(r.Context(), sess.ClassID, skill, hasSkill)
	for _, b := range toolBad {
		obslog.ErrorCtx(r.Context(), "conv.tool_ref_dropped", "技能里的工具引用不可用，已跳过", "skill", skillName, "detail", b)
	}
	var toolLog []map[string]any

	// 生成函数：抽成回调，便于单测 SSE 线格式而不依赖 Qdrant/网关。
	generate := func(onDelta func(string) error, emit func(string, any)) (string, error) {
		msgs := buildMessages(system, hits, content, history)
		if len(toolSpecs) == 0 {
			return h.Chat.CompleteStream(r.Context(), msgs, onDelta)
		}
		return h.runWithTools(r.Context(), runCtx{
			convID: conv.ID, classID: sess.ClassID, skillName: skillName,
			specs: toolSpecs, index: toolIndex, log: &toolLog,
		}, msgs, onDelta, emit)
	}

	msgID, err := h.streamAnswer(r.Context(), w, flusher, conv.ID, hits, skillName, generate)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "conv.stream_failed", "会话流式回答失败", "conv_id", conv.ID, "detail", err.Error())
	}
	// 工具调用留痕到消息行（便于回看这一轮到底调了什么）
	if msgID > 0 && len(toolLog) > 0 {
		if js, err := json.Marshal(toolLog); err == nil {
			if _, err := h.Pool.ExecContext(r.Context(), `UPDATE messages SET tool_calls = ? WHERE id = ?`, string(js), msgID); err != nil {
				obslog.ErrorCtx(r.Context(), "conv.tool_calls_persist_failed", "写入消息的工具调用记录失败", "msg_id", msgID, "detail", err.Error())
			}
		}
	}
}

// streamAnswer 写完整条 SSE 流：meta → delta* → done（或 error），并把助手回答落库。
//
// 契约（课件 stream/answer 页）：
//   - meta.status **恒为 "ok"**；有无依据看 citations 是否为空；
//   - 流已开始后 HTTP 状态码固定 200，后续失败只能补 `event: error`；
//   - 生成中途失败但已产出片段 → 以已产出内容收尾并持久化；零产物才报错；
//   - 检索落空时，推给浏览器前把输出里的 `[n]` 编号剥掉。
func (h *Handlers) streamAnswer(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	convID int64,
	hits []knowledge.Hit,
	skillName string,
	generate func(onDelta func(string) error, emit func(string, any)) (string, error),
) (int64, error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	noEvidence := len(hits) == 0

	// meta：状态恒 ok；技能名 + 引用列表（无依据时为空数组）
	writeEvent(w, flusher, "meta", map[string]any{
		"status":    "ok",
		"skill":     skillName,
		"citations": hits,
	})

	// 无依据时，流式下发前剥掉 [n] 编号（含跨 delta 被切开的编号）
	stripper := &numStripper{}
	onDelta := func(delta string) error {
		out := delta
		if noEvidence {
			out = stripper.feed(delta)
		}
		if out == "" {
			return nil
		}
		writeEvent(w, flusher, "delta", map[string]any{"text": out})
		return nil
	}

	answer, genErr := generate(onDelta, func(event string, data any) {
		writeEvent(w, flusher, event, data)
	})
	if genErr != nil && strings.TrimSpace(answer) == "" {
		// 零产物才报错（流已开始 → HTTP 保持 200，只能补 error 事件）
		writeEvent(w, flusher, "error", map[string]any{"error": "生成回答失败"})
		return 0, genErr
	}
	if noEvidence {
		// 收尾时把滞留在剥离器里的残余也下发，并对落库正文做一次同样的剥离
		if tail := stripper.flush(); tail != "" {
			writeEvent(w, flusher, "delta", map[string]any{"text": tail})
		}
		answer = stripNumbers(answer)
	}

	saveFn := h.save
	if saveFn == nil {
		saveFn = h.saveAssistant
	}
	msgID, err := saveFn(ctx, convID, answer, hits, skillName)
	if err != nil {
		writeEvent(w, flusher, "error", map[string]any{"error": "保存回答失败"})
		return 0, err
	}
	writeEvent(w, flusher, "done", map[string]any{"message_id": msgID})
	return msgID, nil
}

// ── 智能体 / 技能装载与路由 ────────────────────────────────────

// loadAgent 取会话所选智能体的角色提示词与绑定材料编号。
//
// 课件 skill-model 页：绑定关系是两张关联表，不是快照；每次提问重读，教师改了绑定立即生效。
// 提示词为空 → 回退默认角色设定（不拒绝请求）。
func (h *Handlers) loadAgent(ctx context.Context, agentID, classID int64) (agentCfg, error) {
	var cfg agentCfg
	var prompt string
	var tplID sql.NullInt64
	var tplVars []byte
	// 注意：这里**不**校验 enabled —— 已有会话在智能体停用后仍可继续提问（课件 session 页）。
	err := h.Pool.QueryRowContext(ctx,
		`SELECT prompt, template_id, template_vars FROM agents WHERE id = ? AND class_id = ?`,
		agentID, classID).Scan(&prompt, &tplID, &tplVars)
	if err != nil {
		return cfg, err
	}

	// 提示词模板：绑定后按变量渲染，渲染结果排在智能体自己的 prompt 之前。
	// 模板已被删/跨班时静默跳过（不让一句配置漂移把整轮问答打死）。
	if tplID.Valid && tplID.Int64 > 0 {
		var body string
		if err := h.Pool.QueryRowContext(ctx,
			`SELECT body FROM prompt_templates WHERE id = ? AND class_id = ?`, tplID.Int64, classID).Scan(&body); err == nil {
			vars := map[string]string{}
			if len(tplVars) > 0 {
				_ = json.Unmarshal(tplVars, &vars)
			}
			rendered := prompttpl.Render(body, vars)
			if strings.TrimSpace(rendered) != "" {
				prompt = rendered + "\n\n" + strings.TrimSpace(prompt)
			}
		} else {
			obslog.ErrorCtx(ctx, "conv.template_unavailable", "绑定的提示词模板不可用，仅用智能体提示词", "agent_id", agentID, "template_id", tplID.Int64, "detail", err.Error())
		}
	}

	if strings.TrimSpace(prompt) == "" {
		prompt = defaultRolePrompt
	}
	cfg.Prompt = prompt

	rows, err := h.Pool.QueryContext(ctx, `
		SELECT am.material_id
		FROM agent_materials am
		JOIN materials m ON m.id = am.material_id AND m.class_id = ?
		WHERE am.agent_id = ?
		ORDER BY am.material_id ASC`, classID, agentID)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid int64
		if err := rows.Scan(&mid); err != nil {
			return cfg, err
		}
		cfg.MaterialIDs = append(cfg.MaterialIDs, mid)
	}
	return cfg, rows.Err()
}

// loadCandidateSkills 取该智能体绑定且已启用的技能（按 id 升序，供同分取小编号）。
func (h *Handlers) loadCandidateSkills(ctx context.Context, agentID int64) ([]skillCand, error) {
	rows, err := h.Pool.QueryContext(ctx, `
		SELECT sk.id, sk.name, sk.trigger_cond, sk.subject, sk.description, sk.tools
		FROM agent_skills asx
		JOIN skills sk ON sk.id = asx.skill_id
		WHERE asx.agent_id = ? AND sk.enabled = 1
		ORDER BY sk.id ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []skillCand{}
	for rows.Next() {
		var s skillCand
		if err := rows.Scan(&s.ID, &s.Name, &s.Trigger, &s.Subject, &s.Description, &s.Tools); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// triggerSeparators：适用条件按中英文逗号、顿号、分号或竖线切词（课件 route 页）。
func splitTrigger(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；', '|':
			return true
		}
		return false
	})
}

// routeSkill 在候选集内做词法计分，至多选一个。
//
// 规则（课件 route 页）：把适用条件切词，逐词判断是否为提问的子串，命中一个得一分；
// 取最高分；同分取编号较小者（候选按 id 升序，用严格大于保留先出现者）；零分不选。
// 匹配前两侧统一转小写（case folding）。
func routeSkill(cands []skillCand, question string) (skillCand, bool) {
	q := strings.ToLower(question)
	best := 0
	var chosen skillCand
	for _, c := range cands {
		score := 0
		for _, kw := range splitTrigger(c.Trigger) {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" {
				continue
			}
			if strings.Contains(q, kw) {
				score++
			}
		}
		if score > best {
			best = score
			chosen = c
		}
	}
	if best == 0 {
		return skillCand{}, false
	}
	return chosen, true
}

// filterHits 实现步骤 7：去掉空正文、按技能「资料范围」过滤、保持原序（编号由组消息时统一编号）。
func filterHits(hits []knowledge.Hit, skill skillCand, hasSkill bool) []knowledge.Hit {
	out := make([]knowledge.Hit, 0, len(hits))
	for _, h := range hits {
		if strings.TrimSpace(h.Snippet) == "" {
			continue // 去掉空正文
		}
		if hasSkill && skill.Subject != "" && !strings.Contains(h.MaterialTitle, skill.Subject) {
			continue // 学科不符：非空 subject 时只保留标题含该词的材料
		}
		out = append(out, h)
	}
	return out
}

// ── 消息组装 ──────────────────────────────────────────────────

// buildSystem 组装系统消息：**只**放角色提示词、技能说明与边界句（课件 scope 页原文）。
//
// 资料不进系统消息 —— 它是上传来的外部输入，放进系统消息等于给不可信文字发指令的权限。
func buildSystem(prompt string, skill skillCand, hasSkill, hasEvidence bool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	if hasSkill {
		b.WriteString("\n\n【技能：" + skill.Name + "】\n" + strings.TrimSpace(skill.Description))
	}
	b.WriteString("\n\n")
	b.WriteString(boundarySentence(hasEvidence, hasSkill && strings.Contains(skill.Name, guidanceSkillMarker)))
	return b.String()
}

// boundarySentence 生成边界句：引用口径 + 「解题引导」的硬性要求。
func boundarySentence(hasEvidence, guidance bool) string {
	var b strings.Builder
	if hasEvidence {
		b.WriteString("只能依据用户消息中给出的带编号资料回答问题；引用资料时用 [n] 标注对应编号；")
		b.WriteString("资料中没有的信息不要编造。")
	} else {
		b.WriteString("用户消息里没有提供资料。直接按你的角色设定作答，")
		b.WriteString("不要写出 [n] 形式的编号，也不要整段回复「资料中未找到相关内容」。")
	}
	if guidance {
		b.WriteString("本次命中「解题引导」：只指出下一步该检查什么，")
		b.WriteString("不得写出最终答案、选项、得数或完整解题过程。")
	}
	return b.String()
}

// buildMessages 组装送网关的 messages：system + 末尾历史 + 本轮（编号资料 + 问题）。
//
// 课件 scope 页：角色与技能进系统消息，资料进用户消息，附最近 6 条历史。
// history 末尾是本轮刚写入的提问，先剔除它（它要以「资料 + 问题」的形态重新追加）。
func buildMessages(system string, hits []knowledge.Hit, question string, history []message) []knowledge.ChatMessage {
	msgs := []knowledge.ChatMessage{{Role: "system", Content: system}}

	prior := history
	if n := len(prior); n > 0 && prior[n-1].Role == "user" && prior[n-1].Content == question {
		prior = prior[:n-1]
	}
	for _, m := range prior {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		msgs = append(msgs, knowledge.ChatMessage{Role: m.Role, Content: truncateRunes(c, histMsgMaxRunes)})
	}

	var b strings.Builder
	for i, hh := range hits {
		fmt.Fprintf(&b, "[%d] 材料《%s》切片 #%d：\n%s\n\n",
			i+1, hh.MaterialTitle, hh.ChunkIndex, truncateRunes(hh.Snippet, materialMaxRunes))
	}
	b.WriteString("问题：" + question)
	msgs = append(msgs, knowledge.ChatMessage{Role: "user", Content: b.String()})
	return msgs
}

// saveAssistant 落库助手消息，返回自增 id。
func (h *Handlers) saveAssistant(ctx context.Context, convID int64, content string, citations []knowledge.Hit, skillName string) (int64, error) {
	if citations == nil {
		citations = []knowledge.Hit{}
	}
	citJSON, err := json.Marshal(citations)
	if err != nil {
		return 0, err
	}
	res, err := h.Pool.ExecContext(ctx,
		`INSERT INTO messages (conv_id, role, content, citations, skill_name) VALUES (?, 'assistant', ?, ?, ?)`,
		convID, content, string(citJSON), skillName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ── 工具 ──────────────────────────────────────────────────────

// numRefRe 匹配 [数字] 形式的引用编号。
var numRefRe = regexp.MustCompile(`\[\d+\]`)

// stripNumbers 剥掉正文里所有 [n] 编号（无依据分支收尾用）。
func stripNumbers(s string) string {
	return numRefRe.ReplaceAllString(s, "")
}

// numStripper 在流式输出中剥掉 [n] 编号，并处理编号跨 delta 被切开的情况。
//
// 策略：遇到 '[' 起，向后找 ']'；未闭合且已确定是「[数字…」形态就先扣住，等下一段。
type numStripper struct{ buf string }

func (s *numStripper) feed(chunk string) string {
	s.buf += chunk
	var out strings.Builder
	i := 0
	for i < len(s.buf) {
		c := s.buf[i]
		if c != '[' {
			out.WriteByte(c)
			i++
			continue
		}
		rel := strings.IndexByte(s.buf[i:], ']')
		if rel < 0 {
			// 未闭合：已读到 ']' 之前。判断 '[' 之后是否全是数字（可能还没读完）
			after := s.buf[i+1:]
			if allDigits(after) {
				break // 扣住等下一段
			}
			out.WriteByte('[') // 不是 [n …：原样输出
			i++
			continue
		}
		inner := s.buf[i+1 : i+rel]
		if inner != "" && allDigits(inner) {
			i += rel + 1 // 吞掉 [n]
			continue
		}
		out.WriteString(s.buf[i : i+rel+1])
		i += rel + 1
	}
	s.buf = s.buf[i:]
	return out.String()
}

func (s *numStripper) flush() string {
	rest := s.buf
	s.buf = ""
	return rest
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// truncateRunes 按 rune 截断到 n 个字符。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// titleFrom 用问题前 24 个 rune 生成会话标题（课件 session 页）。
func titleFrom(q string) string {
	return strings.TrimSpace(truncateRunes(strings.TrimSpace(q), titleMaxRunes))
}

// writeEvent 写一条 SSE 事件并立即 flush。
func writeEvent(w http.ResponseWriter, f http.Flusher, event string, data any) {
	payload, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	f.Flush()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

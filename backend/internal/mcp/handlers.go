package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/go-sql-driver/mysql"
)

// 名称规则（第 6 课：引用格式「工具名@服务器名」；
// 工具名小写开头、不含连字符；服务器名允许连字符）。
var (
	serverNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	toolNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Handlers 汇总 MCP 管理接口。
type Handlers struct {
	pool *sql.DB
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB) *Handlers { return &Handlers{pool: pool} }

// Server 是服务器对象。**auth_token 永不出现在响应里**，只回 has_auth。
type Server struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	BaseURL string     `json:"base_url"`
	HasAuth bool       `json:"has_auth"`
	Enabled bool       `json:"enabled"`
	Tools   []ToolInfo `json:"tools"`
	Created string     `json:"created_at,omitempty"`
	ToolNum int        `json:"tool_count"`
}

// ToolInfo 是工具缓存的一行。
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Call 是审计记录。
type Call struct {
	ID         int64  `json:"id"`
	ConvID     int64  `json:"conversation_id"`
	SkillName  string `json:"skill_name"`
	ServerName string `json:"server_name"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
	Result     string `json:"result"`
	Status     string `json:"status"`
	ErrorText  string `json:"error,omitempty"`
	DurationMS int    `json:"duration_ms"`
	CreatedAt  string `json:"created_at"`
}

type serverBody struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	AuthToken string `json:"auth_token"`
	Enabled   *bool  `json:"enabled"`
}

// List 处理 GET /api/mcp/servers。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	list, err := h.loadServers(r.Context(), sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "mcp.list_failed", "列出 MCP 服务器失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": list})
}

// Create 处理 POST /api/mcp/servers（仅教师）。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req serverBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	name, base, err := validateServer(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}
	res, err := h.pool.Exec(
		`INSERT INTO mcp_servers (class_id, name, base_url, auth_token, enabled) VALUES (?, ?, ?, ?, ?)`,
		sess.ClassID, name, base, req.AuthToken, enabled)
	if err != nil {
		if isDuplicate(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名服务器已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "mcp.create_failed", "新建 MCP 服务器失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, Server{
		ID: id, Name: name, BaseURL: base, HasAuth: req.AuthToken != "",
		Enabled: enabled == 1, Tools: []ToolInfo{},
	})
}

// Update 处理 PUT /api/mcp/servers/{id}（仅教师）。auth_token 传空串表示清空，不传表示保留。
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	var req serverBody
	if v, ok := raw["auth_token"]; ok {
		// 显式给了 auth_token 字段才覆盖（含空串）
		_ = json.Unmarshal(v, &req.AuthToken)
		req.Name = "__keep__"
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &req.Name)
	}
	if v, ok := raw["base_url"]; ok {
		_ = json.Unmarshal(v, &req.BaseURL)
	}
	if v, ok := raw["enabled"]; ok {
		var b bool
		_ = json.Unmarshal(v, &b)
		req.Enabled = &b
	}

	cur, err := h.fetchServer(r.Context(), id, sess.ClassID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if req.Name == "__keep__" || req.Name == "" {
		req.Name = cur.Name
	}
	if req.BaseURL == "" {
		req.BaseURL = cur.BaseURL
	}
	name, base, err := validateServer(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}

	sets := []string{"name=?", "base_url=?"}
	args := []any{name, base}
	if _, given := raw["auth_token"]; given {
		sets = append(sets, "auth_token=?")
		args = append(args, req.AuthToken)
	}
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
		sets = append(sets, "enabled=?")
		args = append(args, boolToInt(enabled))
	}
	args = append(args, id, sess.ClassID)
	if _, err := h.pool.Exec(`UPDATE mcp_servers SET `+strings.Join(sets, ", ")+` WHERE id=? AND class_id=?`, args...); err != nil {
		if isDuplicate(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名服务器已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "mcp.update_failed", "更新 MCP 服务器失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	// base_url 变了，缓存里的工具就不再可信 → 清掉，等重新连接
	if base != cur.BaseURL {
		_, _ = h.pool.Exec(`DELETE FROM mcp_tools WHERE server_id = ?`, id)
	}
	list, _ := h.loadServers(r.Context(), sess.ClassID)
	for _, s := range list {
		if s.ID == id {
			writeJSON(w, http.StatusOK, s)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// Delete 处理 DELETE /api/mcp/servers/{id}：删服务器与工具缓存，**保留**调用记录。
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	res, err := h.pool.Exec(`DELETE FROM mcp_servers WHERE id=? AND class_id=?`, id, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "mcp.delete_failed", "删除 MCP 服务器失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Connect 处理 POST /api/mcp/servers/{id}/connect：
// 打 initialize + tools/list，成功则整表重写工具缓存；失败回 502。
func (h *Handlers) Connect(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	srv, err := h.fetchServer(r.Context(), id, sess.ClassID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	cl := NewClient(srv.BaseURL, srv.Token)
	if err := cl.Initialize(r.Context()); err != nil {
		obslog.ErrorCtx(r.Context(), "mcp.connect_failed", "连接 MCP 服务器失败", "server", srv.Name, "detail", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "mcp_unreachable", "detail": err.Error()})
		return
	}
	tools, err := cl.ListTools(r.Context())
	if err != nil {
		obslog.ErrorCtx(r.Context(), "mcp.list_tools_failed", "拉取 MCP 工具清单失败", "server", srv.Name, "detail", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "mcp_unreachable", "detail": err.Error()})
		return
	}

	tx, err := h.pool.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM mcp_tools WHERE server_id = ?`, id); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	out := []ToolInfo{}
	for _, t := range tools {
		if _, err := tx.Exec(
			`INSERT INTO mcp_tools (server_id, name, description, input_schema) VALUES (?, ?, ?, ?)`,
			id, t.Name, t.Description, string(t.InputSchema)); err != nil {
			obslog.ErrorCtx(r.Context(), "mcp.tool_cache_write_failed", "写入工具缓存失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		out = append(out, ToolInfo{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server": srv.Name, "protocol": protocolVersion, "tools": out, "count": len(out),
	})
}

// ToolCalls 处理 GET /api/mcp/tool-calls：本班最近 N 条调用记录（默认 50，上限 200）。
func (h *Handlers) ToolCalls(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := h.pool.QueryContext(r.Context(), `
		SELECT id, COALESCE(conversation_id, 0), skill_name, server_name, tool_name,
		       arguments, result, status, error_text, duration_ms, created_at
		FROM mcp_tool_calls WHERE class_id = ? ORDER BY id DESC LIMIT ?`, sess.ClassID, limit)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "mcp.tool_calls_query_failed", "查询工具调用记录失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer rows.Close()

	list := []Call{}
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.ID, &c.ConvID, &c.SkillName, &c.ServerName, &c.ToolName,
			&c.Arguments, &c.Result, &c.Status, &c.ErrorText, &c.DurationMS, &c.CreatedAt); err != nil {
			obslog.ErrorCtx(r.Context(), "mcp.tool_calls_scan_failed", "扫描工具调用行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		list = append(list, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool_calls": list})
}

// ── 供会话链路使用的解析与执行 ────────────────────────────────

// Resolved 是一条「已经解析到具体服务器」的工具。
type Resolved struct {
	ServerID   int64
	ServerName string
	BaseURL    string
	Token      string
	Def        ToolDef
}

// Ref 是技能里写的引用，形如 `unit_convert@demo-tools`。
func (h *Handlers) Ref(server, tool string) string { return tool + "@" + server }

// ResolveTools 把技能里的工具引用解析成可执行工具。
//
// 四类校验（配置期从严，第 6 课）：格式 / 存在 / 可用 / 在缓存清单内。
// 这里返回的错误只用于日志——会话链路里解析不到就**不放这个工具**，
// 不因为一个坏引用把整轮问答打死。
func ResolveTools(ctx context.Context, pool *sql.DB, classID int64, refs []string) ([]Resolved, []string) {
	out := []Resolved{}
	bad := []string{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "knowledge_search" {
			continue // 内置检索，不走 MCP
		}
		tool, server, found := strings.Cut(ref, "@")
		if !found {
			bad = append(bad, ref+"（缺少 @服务器名）")
			continue
		}
		if !toolNameRe.MatchString(tool) || !serverNameRe.MatchString(server) {
			bad = append(bad, ref+"（格式不合法）")
			continue
		}
		var (
			sid   int64
			base  string
			token string
			enab  int
			desc  string
			sch   []byte
		)
		err := pool.QueryRowContext(ctx, `
			SELECT s.id, s.base_url, s.auth_token, s.enabled, t.description, t.input_schema
			FROM mcp_servers s JOIN mcp_tools t ON t.server_id = s.id
			WHERE s.class_id = ? AND s.name = ? AND t.name = ?`, classID, server, tool).
			Scan(&sid, &base, &token, &enab, &desc, &sch)
		if errors.Is(err, sql.ErrNoRows) {
			bad = append(bad, ref+"（服务器或工具不存在，需先连接）")
			continue
		}
		if err != nil {
			bad = append(bad, ref+"（查询失败）")
			continue
		}
		if enab == 0 {
			bad = append(bad, ref+"（服务器已停用）")
			continue
		}
		out = append(out, Resolved{
			ServerID: sid, ServerName: server, BaseURL: base, Token: token,
			Def: ToolDef{Name: tool, Description: desc, InputSchema: sch},
		})
	}
	return out, bad
}

// Execute 执行一次工具调用并写审计行。
//
// 失败也要写审计（status=error）：课件 audit 页要求"失败必须留痕、且失败要回传给模型"。
func (h *Handlers) Execute(ctx context.Context, classID, convID int64, skillName, server, tool, argsJSON string) (string, error) {
	var args map[string]any
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			args = map[string]any{}
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	srv, err := h.fetchServerByName(ctx, server, classID)
	if err != nil {
		h.audit(ctx, classID, convID, skillName, server, tool, argsJSON, "", "error", err.Error(), 0)
		return "", err
	}

	start := time.Now()
	cl := NewClient(srv.BaseURL, srv.Token)
	text, callErr := cl.CallTool(ctx, tool, args)
	elapsed := int(time.Since(start).Milliseconds())

	if callErr != nil {
		h.audit(ctx, classID, convID, skillName, server, tool, argsJSON, "", "error", callErr.Error(), elapsed)
		return "", callErr
	}
	h.audit(ctx, classID, convID, skillName, server, tool, argsJSON, text, "ok", "", elapsed)
	return text, nil
}

func (h *Handlers) audit(ctx context.Context, classID, convID int64, skillName, server, tool, args, result, status, errText string, ms int) {
	var conv any
	if convID > 0 {
		conv = convID
	}
	if _, err := h.pool.ExecContext(ctx, `
		INSERT INTO mcp_tool_calls
		  (class_id, conversation_id, skill_name, server_name, tool_name, arguments, result, status, error_text, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		classID, conv, skillName, server, tool, args, result, status, clip(errText, 480), ms); err != nil {
		obslog.ErrorCtx(ctx, "mcp.audit_write_failed", "写工具调用审计失败", "detail", err.Error())
	}
}

// ── 内部 ────────────────────────────────────────────────────

type serverRow struct {
	ID      int64
	Name    string
	BaseURL string
	Token   string
	Enabled bool
}

func (h *Handlers) fetchServerByName(ctx context.Context, name string, classID int64) (serverRow, error) {
	var s serverRow
	var enab int
	err := h.pool.QueryRowContext(ctx,
		`SELECT id, name, base_url, auth_token, enabled FROM mcp_servers WHERE class_id=? AND name=?`,
		classID, name).Scan(&s.ID, &s.Name, &s.BaseURL, &s.Token, &enab)
	if err != nil {
		return s, err
	}
	if enab == 0 {
		return s, errors.New("MCP 服务器已停用")
	}
	s.Enabled = true
	return s, nil
}

func (h *Handlers) fetchServer(ctx context.Context, id, classID int64) (serverRow, error) {
	var s serverRow
	var enab int
	err := h.pool.QueryRowContext(ctx,
		`SELECT id, name, base_url, auth_token, enabled FROM mcp_servers WHERE id=? AND class_id=?`,
		id, classID).Scan(&s.ID, &s.Name, &s.BaseURL, &s.Token, &enab)
	if err != nil {
		return s, err
	}
	s.Enabled = enab != 0
	return s, nil
}

func (h *Handlers) loadServers(ctx context.Context, classID int64) ([]Server, error) {
	rows, err := h.pool.QueryContext(ctx,
		`SELECT id, name, base_url, auth_token, enabled, created_at
		 FROM mcp_servers WHERE class_id = ? ORDER BY id ASC`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Server{}
	for rows.Next() {
		var s Server
		var token string
		var enab int
		if err := rows.Scan(&s.ID, &s.Name, &s.BaseURL, &token, &enab, &s.Created); err != nil {
			return nil, err
		}
		s.HasAuth = token != ""
		s.Enabled = enab != 0
		s.Tools = []ToolInfo{}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range list {
		trows, err := h.pool.QueryContext(ctx,
			`SELECT name, description, input_schema FROM mcp_tools WHERE server_id = ? ORDER BY name`, list[i].ID)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var t ToolInfo
			var sch []byte
			if err := trows.Scan(&t.Name, &t.Description, &sch); err != nil {
				trows.Close()
				return nil, err
			}
			t.InputSchema = json.RawMessage(sch)
			list[i].Tools = append(list[i].Tools, t)
		}
		trows.Close()
		list[i].ToolNum = len(list[i].Tools)
	}
	return list, nil
}

func validateServer(req serverBody) (name, base string, err error) {
	name = strings.TrimSpace(req.Name)
	base = strings.TrimSpace(req.BaseURL)
	if !serverNameRe.MatchString(name) {
		return "", "", errors.New("服务器名只能用小写字母、数字与连字符（小写开头，≤32 字符）")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return "", "", errors.New("base_url 必须是 http(s) 地址")
	}
	if len(base) > 255 {
		return "", "", errors.New("base_url 过长")
	}
	return name, base, nil
}

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

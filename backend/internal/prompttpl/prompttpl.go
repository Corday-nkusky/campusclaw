// Package prompttpl 承载「提示词模板库」（个人项目补齐项 ②）。
//
// 与第 5 课的 class_prompts（一班一段提示词）的区别：
//
//	class_prompts 是「这个班的助手是谁」的单一设定；
//	提示词模板是**可复用的若干份骨架**，正文里写 {{变量}} 占位，
//	智能体绑定模板 + 一组变量后，每轮会话按变量渲染成系统提示词。
//
// 关键口径（与核验报告一致）：模板正文**会逐字进入送到 LLM 的 system 消息**，
// 占位符在服务端渲染后不复存在；未提供的变量原样保留 {{名字}} 留在正文里
// —— 这样做是为了"缺变量"这件事在报文里肉眼可见，而不是被悄悄删掉。
package prompttpl

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/go-sql-driver/mysql"
)

const (
	maxNameLen  = 64
	maxDescLen  = 200
	maxBodyLen  = 4000
	maxVarsSize = 64
)

// varRe 匹配 {{变量}}；变量名允许中英文、数字、下划线。
var varRe = regexp.MustCompile(`\{\{\s*([\p{Han}A-Za-z0-9_]+)\s*\}\}`)

// Template 是模板对象。
type Template struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Variables   []string `json:"variables"`
	CreatedAt   string   `json:"created_at,omitempty"`
}

// Render 按变量表渲染模板正文。
//
// 未命中的占位符**原样保留**（不删、不置空）：这样"漏填变量"在送给模型的报文里看得见。
func Render(body string, vars map[string]string) string {
	if len(vars) == 0 {
		return body
	}
	return varRe.ReplaceAllStringFunc(body, func(m string) string {
		name := strings.TrimSpace(varRe.FindStringSubmatch(m)[1])
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// ExtractVars 列出正文里出现的占位符（按首次出现顺序去重）。
func ExtractVars(body string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range varRe.FindAllStringSubmatch(body, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// Handlers 汇总模板处理器。
type Handlers struct {
	pool *sql.DB
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB) *Handlers { return &Handlers{pool: pool} }

type body struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Body        string            `json:"body"`
	Variables   map[string]string `json:"variables"` // 仅 /render 用
}

// List 处理 GET /api/prompt-templates：列出本班模板。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	rows, err := h.pool.Query(
		`SELECT id, name, description, body, created_at FROM prompt_templates
		 WHERE class_id = ? ORDER BY id ASC`, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "tpl.list_failed", "列出提示词模板失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer rows.Close()

	list := []Template{}
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Body, &t.CreatedAt); err != nil {
			obslog.ErrorCtx(r.Context(), "tpl.scan_failed", "扫描模板行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		t.Variables = ExtractVars(t.Body)
		list = append(list, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

// Create 处理 POST /api/prompt-templates（仅教师）。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req body
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	name, desc, text, ok2 := validate(w, req)
	if !ok2 {
		return
	}
	res, err := h.pool.Exec(
		`INSERT INTO prompt_templates (class_id, name, description, body) VALUES (?, ?, ?, ?)`,
		sess.ClassID, name, desc, text)
	if err != nil {
		if isDuplicate(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名模板已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "tpl.create_failed", "新建提示词模板失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, Template{ID: id, Name: name, Description: desc, Body: text, Variables: ExtractVars(text)})
}

// Update 处理 PUT /api/prompt-templates/{id}（仅教师）。
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
	var req body
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	name, desc, text, ok2 := validate(w, req)
	if !ok2 {
		return
	}
	res, err := h.pool.Exec(
		`UPDATE prompt_templates SET name=?, description=?, body=? WHERE id=? AND class_id=?`,
		name, desc, text, id, sess.ClassID)
	if err != nil {
		if isDuplicate(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名模板已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "tpl.update_failed", "更新提示词模板失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 内容未变也会是 0，补一次存在性判定
		var x int64
		if err := h.pool.QueryRow(`SELECT id FROM prompt_templates WHERE id=? AND class_id=?`, id, sess.ClassID).Scan(&x); errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
			return
		}
	}
	writeJSON(w, http.StatusOK, Template{ID: id, Name: name, Description: desc, Body: text, Variables: ExtractVars(text)})
}

// Delete 处理 DELETE /api/prompt-templates/{id}（仅教师），成功 204。
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
	// 先解除智能体绑定，再删模板（避免外键报错；也符合"删模板不删智能体"的直觉）
	if _, err := h.pool.Exec(`UPDATE agents SET template_id = NULL WHERE template_id = ?`, id); err != nil {
		obslog.ErrorCtx(r.Context(), "tpl.unbind_failed", "解绑模板失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	res, err := h.pool.Exec(`DELETE FROM prompt_templates WHERE id=? AND class_id=?`, id, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "tpl.delete_failed", "删除提示词模板失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Render 处理 POST /api/prompt-templates/{id}/render：给定变量，返回渲染后的正文。
//
// 存在的意义：让"模板正文最终长什么样"可以**在不调模型的情况下**先看一眼，
// 与真正送进 system 的文本用同一个函数（Render），不会两套逻辑漂移。
func (h *Handlers) Render(w http.ResponseWriter, r *http.Request) {
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
	var req body
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) // 空体也允许

	var text string
	err = h.pool.QueryRow(`SELECT body FROM prompt_templates WHERE id=? AND class_id=?`, id, sess.ClassID).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	rendered := Render(text, req.Variables)
	missing := []string{}
	for _, v := range ExtractVars(rendered) {
		missing = append(missing, v)
	}
	sort.Strings(missing)
	writeJSON(w, http.StatusOK, map[string]any{"rendered": rendered, "unfilled": missing})
}

// ── 内部 ────────────────────────────────────────────────────

func validate(w http.ResponseWriter, req body) (name, desc, text string, ok bool) {
	name = strings.TrimSpace(req.Name)
	desc = strings.TrimSpace(req.Description)
	text = strings.TrimSpace(req.Body)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "名称长度不正确"})
		return "", "", "", false
	}
	if utf8.RuneCountInString(desc) > maxDescLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "说明过长"})
		return "", "", "", false
	}
	if n := utf8.RuneCountInString(text); n < 1 || n > maxBodyLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "模板正文长度不正确"})
		return "", "", "", false
	}
	if len(ExtractVars(text)) > maxVarsSize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "变量过多"})
		return "", "", "", false
	}
	return name, desc, text, true
}

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Package prompt 承载「班级提示词」的读写（官方 contract PRM-001 / PRM-002）。
//
// 提示词规定助手**是谁**（角色），不负责某一类题目的解题规程 —— 后者是技能。
// 一班一行；教师可写、全班可读。
package prompt

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// 契约上限（官方 openapi OpPRM002 描述原文：「去掉空白后为 1 到 2000 个字符」）。
const (
	minPromptLen = 1
	maxPromptLen = 2000
)

// Handlers 汇总提示词处理器。
type Handlers struct {
	pool *sql.DB
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB) *Handlers {
	return &Handlers{pool: pool}
}

type promptBody struct {
	Body string `json:"body"`
}

// Get 处理 GET /api/prompt：返回本班提示词。没有则返回空串（不是 404）。
//
// 官方契约：学生与教师都能读。响应 {"body": "..."}
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var body string
	err := h.pool.QueryRow(
		`SELECT body FROM class_prompts WHERE class_id = ?`, sess.ClassID,
	).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		// 尚未设置：返回空串而非 404（界面据此显示空输入框）
		writeJSON(w, http.StatusOK, promptBody{Body: ""})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "prompt.get_failed", "读取班级提示词失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, promptBody{Body: body})
}

// Put 处理 PUT /api/prompt：保存本班提示词（仅教师）。
//
// 官方契约：去空白后 1~2000 字符；重复保存覆盖原行（幂等）。
func (h *Handlers) Put(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req promptBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}

	body := strings.TrimSpace(req.Body)
	if n := utf8.RuneCountInString(body); n < minPromptLen || n > maxPromptLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "提示词长度不正确"})
		return
	}

	// 幂等保存：class_id 为主键，重复提交覆盖同一行
	if _, err := h.pool.Exec(
		`INSERT INTO class_prompts (class_id, body) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE body = VALUES(body)`,
		sess.ClassID, body,
	); err != nil {
		obslog.ErrorCtx(r.Context(), "prompt.put_failed", "保存班级提示词失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, promptBody{Body: body})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

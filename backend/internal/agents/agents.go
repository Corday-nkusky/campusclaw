// Package agents 承载「智能体」的 CRUD（官方 contract AGT-001~004）。
//
// 一个 agent = 名称 + 提示词 + 一组技能 + 一组材料（后两者是多对多关联表）。
// 这是第 5 课「几个角色扮演的老师」的落点：教师可建多个 agent，学生建会话时选一个。
package agents

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/go-sql-driver/mysql"
)

const (
	maxAgentNameLen   = 64
	maxAgentPromptLen = 2000
)

// Handlers 汇总智能体处理器。
type Handlers struct {
	pool *sql.DB
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB) *Handlers {
	return &Handlers{pool: pool}
}

// Agent 是智能体对象（响应形态与官方 OpAGT001Response 一致，**不含 class_id**）。
//
// template_id / template_vars 是个人项目补齐项：智能体除了自己的 prompt，
// 还可以绑定一份提示词模板；会话时按变量渲染后，模板正文会成为系统提示词的开头。
type Agent struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Prompt       string            `json:"prompt"`
	Enabled      bool              `json:"enabled"`
	TemplateID   int64             `json:"template_id"`
	TemplateVars map[string]string `json:"template_vars"`
	SkillIDs     []int64           `json:"skill_ids"`
	MaterialIDs  []int64           `json:"material_ids"`
}

type agentBody struct {
	Name         string            `json:"name"`
	Prompt       string            `json:"prompt"`
	Enabled      bool              `json:"enabled"`
	TemplateID   int64             `json:"template_id"`
	TemplateVars map[string]string `json:"template_vars"`
	SkillIDs     []int64           `json:"skill_ids"`
	MaterialIDs  []int64           `json:"material_ids"`
}

// List 处理 GET /api/agents。
//
// 官方契约：教师看到本班全部；学生只看已启用的。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	// 学生只列已启用；教师列全部
	q := `SELECT id, name, prompt, enabled, template_id, template_vars FROM agents WHERE class_id = ?`
	args := []any{sess.ClassID}
	if sess.Role != "teacher" {
		q += ` AND enabled = 1`
	}
	q += ` ORDER BY id ASC`

	rows, err := h.pool.Query(q, args...)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "agent.list_failed", "列出智能体失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer rows.Close()

	list := []Agent{}
	for rows.Next() {
		var a Agent
		var enabled int
		var tplID sql.NullInt64
		var tplVars []byte
		if err := rows.Scan(&a.ID, &a.Name, &a.Prompt, &enabled, &tplID, &tplVars); err != nil {
			obslog.ErrorCtx(r.Context(), "agent.scan_failed", "扫描智能体行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		a.Enabled = enabled != 0
		a.TemplateID = tplID.Int64
		a.TemplateVars = decodeVars(tplVars)
		a.SkillIDs, a.MaterialIDs, err = h.loadAssoc(a.ID)
		if err != nil {
			obslog.ErrorCtx(r.Context(), "agent.assoc_load_failed", "读取智能体关联失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.rows_failed", "遍历智能体行失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": list})
}

// Get 处理 GET /api/agents/{id}。跨班与不存在同形 404。
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
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
	a, err := h.fetch(r, sess, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "agent.get_failed", "读取智能体失败", "agent_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// Create 处理 POST /api/agents（仅教师）。同班同名 → 400。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req agentBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if err := h.validateAssoc(sess, req.SkillIDs, req.MaterialIDs); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err := h.validateTemplate(sess, req.TemplateID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	name, prompt, ok2 := validNamePrompt(w, req)
	if !ok2 {
		return
	}

	tx, err := h.pool.BeginTx(r.Context(), nil)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "agent.tx_begin_failed", "开启智能体事务失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO agents (class_id, name, prompt, enabled, template_id, template_vars) VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ClassID, name, prompt, boolToInt(req.Enabled), nullableID(req.TemplateID), encodeVars(req.TemplateVars))
	if err != nil {
		if isDuplicateKey(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名智能体已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "agent.create_failed", "新建智能体失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	id, _ := res.LastInsertId()

	if err := h.writeAssoc(tx, id, req.SkillIDs, req.MaterialIDs); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.assoc_write_failed", "写入智能体关联失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if err := tx.Commit(); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.tx_commit_failed", "提交智能体事务失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	writeJSON(w, http.StatusCreated, Agent{
		ID: id, Name: name, Prompt: prompt, Enabled: req.Enabled,
		TemplateID: req.TemplateID, TemplateVars: nonNilVars(req.TemplateVars),
		SkillIDs: nonNil(req.SkillIDs), MaterialIDs: nonNil(req.MaterialIDs),
	})
}

// Update 处理 PUT /api/agents/{id}（仅教师）。跨班/不存在同 404。
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

	var req agentBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if err := h.validateAssoc(sess, req.SkillIDs, req.MaterialIDs); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err := h.validateTemplate(sess, req.TemplateID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	name, prompt, ok2 := validNamePrompt(w, req)
	if !ok2 {
		return
	}

	tx, err := h.pool.BeginTx(r.Context(), nil)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "agent.tx_begin_failed", "开启智能体事务失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`UPDATE agents SET name=?, prompt=?, enabled=?, template_id=?, template_vars=? WHERE id=? AND class_id=?`,
		name, prompt, boolToInt(req.Enabled), nullableID(req.TemplateID), encodeVars(req.TemplateVars), id, sess.ClassID)
	if err != nil {
		if isDuplicateKey(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名智能体已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "agent.update_failed", "更新智能体失败", "agent_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 可能是不存在，也可能是内容未变（MySQL RowsAffected=0）。再取一次判定。
		var exists int64
		if err := tx.QueryRow(`SELECT id FROM agents WHERE id=? AND class_id=?`, id, sess.ClassID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
			return
		}
	}

	// 关联整体替换：先删后插
	if _, err := tx.Exec(`DELETE FROM agent_skills WHERE agent_id=?`, id); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.assoc_clear_failed", "清空智能体关联失败", "target", "skills", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if _, err := tx.Exec(`DELETE FROM agent_materials WHERE agent_id=?`, id); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.assoc_clear_failed", "清空智能体关联失败", "target", "materials", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if err := h.writeAssoc(tx, id, req.SkillIDs, req.MaterialIDs); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.assoc_write_failed", "写入智能体关联失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if err := tx.Commit(); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.tx_commit_failed", "提交智能体事务失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, Agent{
		ID: id, Name: name, Prompt: prompt, Enabled: req.Enabled,
		TemplateID: req.TemplateID, TemplateVars: nonNilVars(req.TemplateVars),
		SkillIDs: nonNil(req.SkillIDs), MaterialIDs: nonNil(req.MaterialIDs),
	})
}

// Delete 删除一个智能体（AGT-004 之外的补齐项）。
//
// 为什么必须有：教师建错角色时若只能改不能删，列表很快堆满「模板助教-123456」
// 这类一次性的东西，配置页就没法看了。
//
// 关联表（agent_skills / agent_materials）是 ON DELETE CASCADE，随主行一并清掉；
// 但 conversations.agent_id 是 NO ACTION —— 已被会话引用的智能体不能删，
// 否则历史会话的 agent 指向会断。这种情况回 409 并说明原因，而不是抛 503。
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

	// 先按 class 校验归属：别的班的 id 一律当作不存在
	var exists int64
	err = h.pool.QueryRow(`SELECT id FROM agents WHERE id=? AND class_id=?`, id, sess.ClassID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "agent.delete_lookup_failed", "查询智能体失败", "agent_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	var used int64
	if err := h.pool.QueryRow(`SELECT COUNT(*) FROM conversations WHERE agent_id=?`, id).Scan(&used); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.delete_usage_failed", "统计智能体会话数失败", "agent_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if used > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "该智能体已被会话使用，不能删除",
			"detail": "停用它即可让它不再出现在学生的可选列表里，历史会话仍可回看。",
			"conversations": used,
		})
		return
	}

	if _, err := h.pool.Exec(`DELETE FROM agents WHERE id=? AND class_id=?`, id, sess.ClassID); err != nil {
		obslog.ErrorCtx(r.Context(), "agent.delete_failed", "删除智能体失败", "agent_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	obslog.InfoCtx(r.Context(), "agent.deleted", "智能体已删除", "agent_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// ── 内部 ───────────────────────────────────────────────────────

// fetch 按 id + class 取一个 agent（fetch-then-check）。
func (h *Handlers) fetch(r *http.Request, sess *auth.Session, id int64) (Agent, error) {
	var a Agent
	var enabled int
	var tplID sql.NullInt64
	var tplVars []byte
	err := h.pool.QueryRow(
		`SELECT id, name, prompt, enabled, template_id, template_vars FROM agents WHERE id=? AND class_id=?`, id, sess.ClassID,
	).Scan(&a.ID, &a.Name, &a.Prompt, &enabled, &tplID, &tplVars)
	if err != nil {
		return a, err
	}
	a.Enabled = enabled != 0
	a.TemplateID = tplID.Int64
	a.TemplateVars = decodeVars(tplVars)
	a.SkillIDs, a.MaterialIDs, err = h.loadAssoc(a.ID)
	return a, err
}

func (h *Handlers) loadAssoc(agentID int64) (skillIDs, materialIDs []int64, err error) {
	skillIDs = []int64{}
	materialIDs = []int64{}

	sr, err := h.pool.Query(`SELECT skill_id FROM agent_skills WHERE agent_id=? ORDER BY skill_id`, agentID)
	if err != nil {
		return nil, nil, err
	}
	defer sr.Close()
	for sr.Next() {
		var id int64
		if err := sr.Scan(&id); err != nil {
			return nil, nil, err
		}
		skillIDs = append(skillIDs, id)
	}

	mr, err := h.pool.Query(`SELECT material_id FROM agent_materials WHERE agent_id=? ORDER BY material_id`, agentID)
	if err != nil {
		return nil, nil, err
	}
	defer mr.Close()
	for mr.Next() {
		var id int64
		if err := mr.Scan(&id); err != nil {
			return nil, nil, err
		}
		materialIDs = append(materialIDs, id)
	}
	return skillIDs, materialIDs, nil
}

// validateAssoc 确保引用的 skill / material 都属于本班（官方契约：否则 404）。
func (h *Handlers) validateAssoc(sess *auth.Session, skillIDs, materialIDs []int64) error {
	for _, sid := range skillIDs {
		var x int64
		err := h.pool.QueryRow(`SELECT id FROM skills WHERE id=? AND class_id=?`, sid, sess.ClassID).Scan(&x)
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
	}
	for _, mid := range materialIDs {
		var x int64
		err := h.pool.QueryRow(`SELECT id FROM materials WHERE id=? AND class_id=?`, mid, sess.ClassID).Scan(&x)
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Handlers) writeAssoc(tx *sql.Tx, agentID int64, skillIDs, materialIDs []int64) error {
	for _, sid := range skillIDs {
		if _, err := tx.Exec(`INSERT IGNORE INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, agentID, sid); err != nil {
			return err
		}
	}
	for _, mid := range materialIDs {
		if _, err := tx.Exec(`INSERT IGNORE INTO agent_materials (agent_id, material_id) VALUES (?, ?)`, agentID, mid); err != nil {
			return err
		}
	}
	return nil
}

// validNamePrompt 校验名称与提示词长度，失败时已写响应并返回 ok=false。
func validNamePrompt(w http.ResponseWriter, req agentBody) (name, prompt string, ok bool) {
	name = strings.TrimSpace(req.Name)
	prompt = strings.TrimSpace(req.Prompt)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxAgentNameLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "名称长度不正确"})
		return "", "", false
	}
	if n := utf8.RuneCountInString(prompt); n > maxAgentPromptLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "提示词长度不正确"})
		return "", "", false
	}
	// 提示词来源至少要有一个：智能体自身的 prompt，或它绑定的提示词模板。
	// 两者都空时角色设定会退到兜底文案 —— 技术上能跑，但用户显然是想配置点什么，
	// 在配置期直接拒绝比让他上线后才发现"提示词没生效"更好。
	if prompt == "" && req.TemplateID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "bad_request", "detail": "提示词与提示词模板至少要填一个"})
		return "", "", false
	}
	return name, prompt, true
}

func nonNil(xs []int64) []int64 {
	if xs == nil {
		return []int64{}
	}
	return xs
}

func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// validateTemplate 校验绑定的提示词模板属于本班（否则 404，与"资源不存在"同形）。
func (h *Handlers) validateTemplate(sess *auth.Session, templateID int64) error {
	if templateID <= 0 {
		return nil
	}
	var x int64
	err := h.pool.QueryRow(`SELECT id FROM prompt_templates WHERE id=? AND class_id=?`, templateID, sess.ClassID).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return err
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func encodeVars(v map[string]string) any {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

func decodeVars(b []byte) map[string]string {
	out := map[string]string{}
	if len(b) == 0 {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func nonNilVars(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

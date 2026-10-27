// Package skills 承载「技能」的 CRUD（官方 contract SKL-001~004）。
//
// 技能规定**这一类题怎么导**（区别于提示词规定的"助手是谁"）。
// 技能以「技能文件」形式录入：空行以上是头，空行以下是说明。
//
// 技能文件格式（官方 openapi OpSKL002 example 原文）：
//
//	名称：方程解题引导
//	适用条件：方程,移项
//	工具：knowledge_search
//	资料：数学
//	启用：是
//
//	先让学生说出等号两边要保持相等。
package skills

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/go-sql-driver/mysql"
)

// 契约上限（官方 openapi + 第 5 课课件 skill-model 页：
// 名称 ≤32、适用条件 ≤120、说明 ≤2000、资料范围 ≤32、整份 ≤8000）。
const (
	maxNameLen        = 32
	maxTriggerLen     = 120
	maxDescriptionLen = 2000
	maxSubjectLen     = 32
	maxSourceLen      = 8000
	// builtinTool 是内置检索工具名；除此之外一律是 `工具名@服务器名`（第 6 课）。
	builtinTool = "knowledge_search"
)

// toolRefRe 是 `工具名@服务器名` 的形状检查（配置期从严）。
var toolRefRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}@[a-z0-9][a-z0-9-]{0,31}$`)

// Handlers 汇总技能处理器。
type Handlers struct {
	pool *sql.DB
}

// NewHandlers 构造。
func NewHandlers(pool *sql.DB) *Handlers {
	return &Handlers{pool: pool}
}

// Skill 是技能对象（响应形态与官方 OpSKL001Response 一致）。
type Skill struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Trigger     string   `json:"trigger"`
	Subject     string   `json:"subject"`
	Tools       []string `json:"tools"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
}

// 解析出的技能文件各段。
type parsedSkill struct {
	name        string
	trigger     string
	subject     string
	tools       []string
	enabled     bool
	description string
}

// parseSkillFile 把技能文件原文解析成结构化字段。
//
// 格式：空行以上的头（每行「键：值」），空行以下的正文即 description。
// 键用中文（与官方 example 一致）：名称 / 适用条件 / 工具 / 资料 / 启用。
//
// 校验（第 5 课课件 skill-model 页 + apply 页"常见故障"）：
//   - 名称、适用条件、启用 **三项必填**；
//   - 空行以下**必须有正文**，否则拒绝；
//   - **重复字段、未定义字段**一律拒绝（给出面向教师的可读错误）；
//   - 元数据区的空行与 `#` 开头的注释行忽略；
//   - 工具写 knowledge_search，或 `工具名@服务器名`（第 6 课口径，形状在此校验，
//     服务器/工具是否存在由 validateTools 查库确认）；
//   - 各字段与整份文件有长度上限。
func parseSkillFile(source string) (parsedSkill, error) {
	var p parsedSkill

	// 找第一个空行：其上为头，其下为说明。
	normalized := strings.ReplaceAll(source, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	splitAt := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			splitAt = i
			break
		}
	}
	if splitAt < 0 {
		return p, errors.New("技能文件需要一个空行，空行下面写技能说明")
	}

	head := lines[:splitAt]
	p.description = strings.TrimSpace(strings.Join(lines[splitAt+1:], "\n"))

	seen := map[string]bool{}
	for _, ln := range head {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue // 元数据区的空行与注释行忽略
		}
		key, val, found := strings.Cut(ln, "：") // 全角冒号
		if !found {
			key, val, found = strings.Cut(ln, ":") // 容忍半角
		}
		if !found {
			return p, fmt.Errorf("技能文件行格式不正确（应为「键：值」）：%s", ln)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if seen[key] {
			return p, fmt.Errorf("字段重复：%s", key)
		}
		seen[key] = true
		switch key {
		case "名称":
			p.name = val
		case "适用条件":
			p.trigger = val
		case "资料":
			p.subject = val
		case "启用":
			switch val {
			case "是":
				p.enabled = true
			case "否":
				p.enabled = false
			default:
				return p, errors.New("启用只能写是或否")
			}
		case "工具":
			for _, t := range strings.Split(val, ",") {
				t = strings.TrimSpace(t)
				if t == "" {
					continue
				}
				if t != builtinTool && !toolRefRe.MatchString(t) {
					return p, fmt.Errorf("工具需写成 %s 或「工具名@服务器名」（工具名小写开头、服务器名可含连字符）：%s", builtinTool, t)
				}
				p.tools = append(p.tools, t)
			}
		default:
			// 未定义字段：拒绝（课件 apply 页明写「未定义字段」应被拒并给出可读提示）
			return p, fmt.Errorf("未定义字段：%s", key)
		}
	}
	if p.tools == nil {
		p.tools = []string{}
	}

	// 必填与长度校验（全部按 rune 计，与契约"字符"一致）
	if p.name == "" {
		return p, errors.New("技能文件缺少名称")
	}
	if !seen["适用条件"] {
		return p, errors.New("技能文件缺少适用条件")
	}
	if !seen["启用"] {
		return p, errors.New("技能文件缺少启用")
	}
	if p.description == "" {
		return p, errors.New("技能文件缺少正文（空行以下的说明）")
	}
	if utf8.RuneCountInString(p.name) > maxNameLen {
		return p, fmt.Errorf("名称最长 %d 个字符", maxNameLen)
	}
	if utf8.RuneCountInString(p.trigger) > maxTriggerLen {
		return p, fmt.Errorf("适用条件最长 %d 个字符", maxTriggerLen)
	}
	if utf8.RuneCountInString(p.subject) > maxSubjectLen {
		return p, fmt.Errorf("资料范围最长 %d 个字符", maxSubjectLen)
	}
	if utf8.RuneCountInString(p.description) > maxDescriptionLen {
		return p, fmt.Errorf("技能说明最长 %d 个字符", maxDescriptionLen)
	}
	if utf8.RuneCountInString(source) > maxSourceLen {
		return p, fmt.Errorf("整份技能文件最长 %d 个字符", maxSourceLen)
	}
	return p, nil
}

type skillFileBody struct {
	Source string `json:"source"`
}

// isDuplicateKey 判定 MySQL 唯一键冲突（错误码 1062）。
func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// List 处理 GET /api/skills：列出本班技能。无技能时返回空数组。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	rows, err := h.pool.Query(
		`SELECT id, name, trigger_cond, subject, tools, enabled, description, source
		 FROM skills WHERE class_id = ? ORDER BY id ASC`, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "skill.list_failed", "列出技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	defer rows.Close()

	list := []Skill{}
	for rows.Next() {
		var s Skill
		var toolsCSV string
		var enabled int
		if err := rows.Scan(&s.ID, &s.Name, &s.Trigger, &s.Subject, &toolsCSV, &enabled, &s.Description, &s.Source); err != nil {
			obslog.ErrorCtx(r.Context(), "skill.scan_failed", "扫描技能行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
		s.Enabled = enabled != 0
		s.Tools = splitCSV(toolsCSV)
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		obslog.ErrorCtx(r.Context(), "skill.rows_failed", "遍历技能行失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": list})
}

// Create 处理 POST /api/skills：用技能文件新建（仅教师）。
//
// 官方契约：非幂等，每次都新增；同班同名返回 400。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req skillFileBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	p, err := parseSkillFile(req.Source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	if err := h.validateTools(sess.ClassID, p.tools); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}

	id, err := h.insertSkill(sess.ClassID, p, req.Source)
	if err != nil {
		if isDuplicateKey(err) {
			// 同班同名 → 400（官方契约明写"不是 409"）
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名技能已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "skill.create_failed", "新建技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, toSkill(id, p, req.Source))
}

// insertSkill 落一行技能，返回自增 id。
func (h *Handlers) insertSkill(classID int64, p parsedSkill, source string) (int64, error) {
	res, err := h.pool.Exec(
		`INSERT INTO skills (class_id, name, trigger_cond, subject, tools, enabled, description, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		classID, p.name, p.trigger, p.subject, strings.Join(p.tools, ","), boolToInt(p.enabled), p.description, source,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// validateTools 做工具引用的「存在 / 可用 / 在缓存清单内」三类校验。
//
// 格式已在解析期查过；这里补齐需要查库的部分（第 6 课：配置期从严）。
// 注意局限：校验与真正调用之间存在时间差（TOCTOU）——工具可能在两刻之间被删或停用，
// 所以会话链路里仍要再解析一次，解析不到就跳过该工具。
func (h *Handlers) validateTools(classID int64, tools []string) error {
	for _, ref := range tools {
		if ref == builtinTool {
			continue
		}
		tool, server, found := strings.Cut(ref, "@")
		if !found {
			return fmt.Errorf("工具 %s 缺少 @服务器名", ref)
		}
		var enabled int
		err := h.pool.QueryRow(`
			SELECT s.enabled FROM mcp_servers s
			JOIN mcp_tools t ON t.server_id = s.id
			WHERE s.class_id = ? AND s.name = ? AND t.name = ?`, classID, server, tool).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("工具 %s 未登记：请先在 MCP 服务器里连接一次，把工具清单同步进来", ref)
		}
		if err != nil {
			return fmt.Errorf("校验工具 %s 失败", ref)
		}
		if enabled == 0 {
			return fmt.Errorf("工具 %s 所属服务器已停用", ref)
		}
	}
	return nil
}

// Upload 处理 POST /api/skills/upload：**上传技能文件**（.md / .txt，仅教师）。
//
// 与 POST /api/skills 的区别只在入口形态：那个收 JSON 里的 source 字符串，
// 这个收 multipart 文件。解析、校验、落库走的是同一套代码——
// 上传路径不能有第二套解析规则，否则"上传的和粘贴的行为不一致"。
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "请用 multipart/form-data 上传，字段名 file"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "缺少文件字段 file"})
		return
	}
	defer func() { _ = file.Close() }()

	name := strings.ToLower(hdr.Filename)
	if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".txt") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "只接受 .md 或 .txt 技能文件"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "读取上传文件失败"})
		return
	}
	source := strings.TrimPrefix(string(raw), "\ufeff") // 去 BOM
	p, err := parseSkillFile(source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	if err := h.validateTools(sess.ClassID, p.tools); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	id, err := h.insertSkill(sess.ClassID, p, source)
	if err != nil {
		if isDuplicateKey(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名技能已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "skill.upload_failed", "上传技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	out := toSkill(id, p, source)
	writeJSON(w, http.StatusCreated, map[string]any{"skill": out, "filename": hdr.Filename})
}

// Update 处理 PUT /api/skills/{id}：用技能文件改写（仅教师）。
//
// 官方契约：幂等覆盖；跨班/不存在同 404；改成与另一条同名 → 400。
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

	var req skillFileBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	p, err := parseSkillFile(req.Source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	if err := h.validateTools(sess.ClassID, p.tools); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}

	// 先按 id + class 取行（fetch-then-check，跨班与不存在同形 404）
	var exists int64
	err = h.pool.QueryRow(`SELECT id FROM skills WHERE id = ? AND class_id = ?`, id, sess.ClassID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	if err != nil {
		obslog.ErrorCtx(r.Context(), "skill.query_failed", "查询技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}

	if _, err := h.pool.Exec(
		`UPDATE skills SET name=?, trigger_cond=?, subject=?, tools=?, enabled=?, description=?, source=?
		 WHERE id=? AND class_id=?`,
		p.name, p.trigger, p.subject, strings.Join(p.tools, ","), boolToInt(p.enabled), p.description, req.Source, id, sess.ClassID,
	); err != nil {
		if isDuplicateKey(err) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "同名技能已存在"})
			return
		}
		obslog.ErrorCtx(r.Context(), "skill.update_failed", "更新技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, toSkill(id, p, req.Source))
}

// Delete 处理 DELETE /api/skills/{id}：删除（仅教师），成功 204 无体。
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

	res, err := h.pool.Exec(`DELETE FROM skills WHERE id = ? AND class_id = ?`, id, sess.ClassID)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "skill.delete_failed", "删除技能失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "资源不存在"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 辅助 ───────────────────────────────────────────────────────

func toSkill(id int64, p parsedSkill, source string) Skill {
	return Skill{
		ID:          id,
		Name:        p.name,
		Trigger:     p.trigger,
		Subject:     p.subject,
		Tools:       p.tools,
		Enabled:     p.enabled,
		Description: p.description,
		Source:      source,
	}
}

func splitCSV(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
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

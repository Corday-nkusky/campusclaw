// Package materials 承载材料列表、详情、下载与上传的 HTTP 处理器。
//
// 实现进度（本文件承载步 4 与步 5）：
//
//	步 4：班级隔离 —— 列表 / 详情 / 下载。
//	步 5：上传入库 —— Upload，同事务写 materials + knowledge_entries。
//
// 进度真相源只有 openspec/changes/add-auth-rbac-class-knowledge/tasks.md 的方括号。
//
// ── 班级隔离的两条路径（design.md D4）────────────────────────
//
//	列表：SQL 层强制 `WHERE class_id = <会话班级>`。
//	按 ID：**不在 SQL 里提前过滤班级**，先取行、再在 Go 里核对 class_id。
//
// 为什么按 ID 不能提前过滤：若 SQL 就把跨班记录筛掉，跨班访问会得到空结果，
// 与「资源不存在」在代码里无法区分，也就**统一不了同形 404**（R5.2）。
//
// ── 上传的三条前置校验（design.md D5 / ADR-3）───────────────
//
// 顺序是刻意的，改顺序会破坏 R4.3：
//
//	① 大小上限用 http.MaxBytesReader。**不能用 ParseMultipartForm 的 maxMemory**——
//	   那个参数只决定「多少留在内存、多少溢写到临时文件」，它不拒绝任何请求体。
//	   实测（探针见 docs/作业-0923-文件上传逻辑分析.md）：maxMemory 设 1 MiB、
//	   上传 5 MiB 文件时 err=nil，服务端仍完整读到 5242880 字节。
//	② 扩展名白名单只认 .txt / .md，且**必须 ToLower**：filepath.Ext 保留原始大小写，
//	   "UPPER.MD" 得到的是 ".MD"，裸比较会把正常文件误拒。
//	③ 内容必须非空且是合法 UTF-8。
//
// 注意白名单与「服务端生成存储名」是**两条正交的防线**：扩展名校验完全不防路径穿越
// （filepath.Ext("../../evil.md") == ".md"，照样通过白名单）。
package materials

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// notFoundBody 是「跨班访问」与「ID 不存在」**共用**的响应体。
//
// 两者必须逐字节相同（R5.2）：任何差异都会变成「该 ID 是否存在」的探测信道
// （design.md D3 / ADR-2）。排障靠服务端日志，不靠响应。
const notFoundBody = `{"error":"not_found"}`

// Handlers 汇总材料相关的处理器。
type Handlers struct {
	pool           *sql.DB
	uploadDir      string
	maxUploadBytes int64
	indexer        *knowledge.Indexer // 可选：nil = 不建检索索引（索引失败不影响上传成功语义）
}

// NewHandlers 构造处理器。
//
// maxUploadBytes 来自 MAX_UPLOAD_BYTES，是上传的**硬上限**（不是建议值）。
func NewHandlers(pool *sql.DB, uploadDir string, maxUploadBytes int64) *Handlers {
	return &Handlers{pool: pool, uploadDir: uploadDir, maxUploadBytes: maxUploadBytes}
}

// SetIndexer 注入检索索引器（add-class-scoped-retrieval tasks 5.1）。
func (h *Handlers) SetIndexer(ix *knowledge.Indexer) { h.indexer = ix }

// material 是列表项。**刻意不含 stored_name**（R5.3）。
type material struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	SizeBytes  int64     `json:"size_bytes"`
	UploaderID int64     `json:"uploader_id"`
	Uploader   string    `json:"uploader"`
	CreatedAt  time.Time `json:"created_at"`
}

// List 处理 GET /api/materials。
//
// 隔离要点：
//   - 查询**强制带** `WHERE m.class_id = ?`，班级取自会话，忽略请求里的一切 class 参数；
//   - 数据来自数据库查询，不硬编码演示条目；
//   - 搜索只在本班范围内过滤（R5.1）—— 搜索条件与班级条件是 **AND** 关系，
//     不存在「先搜全库再筛」的写法。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))

	// 班级条件写在最前面，搜索条件只能追加——顺序固定，改不动隔离。
	sb := strings.Builder{}
	sb.WriteString(`SELECT m.id, m.title, m.size_bytes, m.uploader_id, u.username, m.created_at
		FROM materials m
		JOIN users u ON u.id = m.uploader_id
		WHERE m.class_id = ?`)
	args := []any{sess.ClassID}

	if q != "" {
		sb.WriteString(` AND m.title LIKE CONCAT('%', ?, '%') ESCAPE '\\'`)
		args = append(args, escapeLike(q))
	}
	sb.WriteString(` ORDER BY m.created_at DESC, m.id DESC`)

	rows, err := h.pool.Query(sb.String(), args...)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "material.list_failed", "查询材料列表失败", "class_id", sess.ClassID, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
		return
	}
	defer func() { _ = rows.Close() }()

	list := make([]material, 0, 8)
	for rows.Next() {
		var m material
		if err := rows.Scan(&m.ID, &m.Title, &m.SizeBytes, &m.UploaderID, &m.Uploader, &m.CreatedAt); err != nil {
			obslog.ErrorCtx(r.Context(), "material.scan_failed", "读取材料行失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
			return
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		obslog.ErrorCtx(r.Context(), "material.rows_failed", "遍历材料列表失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
		return
	}

	// 显式回报「这次查询的范围是哪一班」——让隔离结果可自证，不靠界面猜。
	writeJSON(w, http.StatusOK, map[string]any{
		"class_id":   sess.ClassID,
		"class_name": sess.ClassName,
		"query":      q,
		"count":      len(list),
		"materials":  list,
	})
}

// Detail 处理 GET /api/materials/{id}。
//
// fetch-then-check：先按 ID 取行（**不带班级条件**），再核对 class_id。
// 跨班与不存在走同一个 notFound，响应体不含标题、正文与磁盘路径。
func (h *Handlers) Detail(w http.ResponseWriter, r *http.Request) {
	sess, row, ok := h.loadGuarded(w, r)
	if !ok {
		return
	}

	// 正文在 knowledge_entries 里。没有正文不算错误——材料可以尚未解析入库。
	var body string
	err := h.pool.QueryRow(
		`SELECT body FROM knowledge_entries WHERE material_id = ? ORDER BY id DESC LIMIT 1`, row.ID,
	).Scan(&body)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		obslog.ErrorCtx(r.Context(), "material.detail_body_failed", "读取知识库正文失败", "material_id", row.ID, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
		return
	}

	// 逐字段显式构造：响应里没有 stored_name，就没有路径泄漏的可能（R5.3）。
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          row.ID,
		"title":       row.Title,
		"class_id":    row.ClassID,
		"class_name":  row.ClassName,
		"size_bytes":  row.SizeBytes,
		"uploader_id": row.UploaderID,
		"uploader":    row.Uploader,
		"created_at":  row.CreatedAt,
		"body":        body,
		"viewer": map[string]any{
			"user_id":  sess.UserID,
			"username": sess.Username,
			"role":     sess.Role,
			"class_id": sess.ClassID,
		},
	})
}

// File 处理 GET /api/materials/{id}/file。
//
// 与 Detail 完全相同的隔离判定，通过后才碰磁盘。
// `/uploads` 目录**不做静态暴露**（R5.2）——文件只能经这条鉴权路径读出。
func (h *Handlers) File(w http.ResponseWriter, r *http.Request) {
	_, row, ok := h.loadGuarded(w, r)
	if !ok {
		return
	}

	// 磁盘存储名由服务端生成，但仍做一次基底名校验：多一道防线，避免任何
	// 历史数据或人工改库把 `../` 带进来（R4.5 的同一条思路）。
	name := filepath.Base(row.StoredName)
	if name != row.StoredName || name == "." || name == ".." {
		obslog.ErrorCtx(r.Context(), "material.stored_name_invalid", "stored_name 形态非法，拒绝读取", "material_id", row.ID, "stored_name", row.StoredName)
		writeNotFound(w)
		return
	}

	full := filepath.Join(h.uploadDir, name)
	f, err := os.Open(full)
	if err != nil {
		// 文件缺失对调用方与「材料不存在」表现一致：不透露磁盘状态。
		obslog.ErrorCtx(r.Context(), "material.file_unreadable", "材料文件不可读", "material_id", row.ID, "detail", err.Error())
		writeNotFound(w)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		obslog.ErrorCtx(r.Context(), "material.file_stat_failed", "材料文件状态异常", "material_id", row.ID, "detail", err.Error())
		writeNotFound(w)
		return
	}

	if ct := contentTypeFor(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// 强制下载而非内联渲染 + nosniff：材料正文是用户可控内容，
	// 若被浏览器当 HTML 渲染就是存储型 XSS。这两行是那件事的防线。
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(row.Title))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(w, r, name, stat.ModTime(), f)
}

// Upload 处理 POST /api/materials：教师专属，解析正文并与材料记录同事务写入。
//
// 必须经 RequireSession + RequireTeacher 包裹后使用（R3.1、R3.2）。
// 学生会话到不了这里——授权在中间件判定，前端隐藏入口只降低可发现性。
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}

	// ① 上限前置（R4.3）：MaxBytesReader 是**唯一真正的大小上限**。
	// 超限时它在读取阶段就返回错误，请求体不会被完整读进内存。
	// （ParseMultipartForm 的 maxMemory 不是上限——见包注释实测数据。）
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)

	// maxMemory 只决定内存/临时文件的分界，取一个小值即可。
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			obslog.ErrorCtx(r.Context(), "material.upload_too_large", "上传被拒：请求体超过上限", "limit_bytes", h.maxUploadBytes, "user", sess.Username)
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error":       "payload_too_large",
				"limit_bytes": h.maxUploadBytes,
			})
			return
		}
		obslog.ErrorCtx(r.Context(), "material.multipart_failed", "multipart 解析失败", "user", sess.Username, "detail", err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "bad_request", "detail": "multipart 解析失败",
		})
		return
	}
	// 解析过程会在系统临时目录落文件，无论成败都要清掉。
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	// R4.7：表单里若塞了 class_id，**刻意读出来只记日志**，落库一律用会话班级。
	// 这里不做「校验它是否等于会话班级」——只要它能影响落库结果就是漏洞。
	if injected := r.FormValue("class_id"); injected != "" {
		obslog.InfoCtx(r.Context(), "material.class_id_ignored", "忽略表单里的 class_id，落库固定用会话班级", "client_class_id", injected, "user", sess.Username, "class_id", sess.ClassID)
	}

	src, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "bad_request", "detail": "缺少 file 字段",
		})
		return
	}
	defer func() { _ = src.Close() }()

	// ② 扩展名白名单（R4.2）。ToLower 不可省——理由见包注释。
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".txt" && ext != ".md" {
		obslog.ErrorCtx(r.Context(), "material.upload_ext_rejected", "上传被拒：扩展名不在白名单", "ext", ext, "user", sess.Username, "filename", hdr.Filename)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "unsupported_type",
			"allowed": []string{".txt", ".md"},
		})
		return
	}

	// ③ 内容校验（R4.4）。走到这里已保证不超限。
	raw, err := io.ReadAll(src)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error": "payload_too_large", "limit_bytes": h.maxUploadBytes,
			})
			return
		}
		obslog.ErrorCtx(r.Context(), "material.upload_read_failed", "读取上传内容失败", "user", sess.Username, "detail", err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "bad_request", "detail": "读取上传内容失败",
		})
		return
	}
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "empty_content", "detail": "材料内容不能为空",
		})
		return
	}
	if !utf8.Valid(raw) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid_encoding", "detail": "材料内容必须是合法 UTF-8",
		})
		return
	}

	// ③b 剥掉开头 UTF-8 BOM（U+FEFF）。**这一步是实测发现缺了会出问题的，不是洁癖。**
	//
	// 证据：把 `\xEF\xBB\xBF# 标题\n\n正文\n` 传进来，BOM 原样落盘、原样进
	// knowledge_entries.body（实测 HEX 开头就是 EFBBBF），而后端返回的正文交给
	// markdown-it 15.0.2 渲染时，实测得到 `<p>\uFEFF# 标题</p>` ——
	// **一级标题退化成了一行带井号的普通段落**（同一段内容去掉 BOM 则正确得到 `<h1>`）。
	//
	// 这不是假想场景：Windows 记事本保存 UTF-8 默认就带 BOM，
	// 教师用它写一份 .md 再上传，看到的标题就是坏的。
	// 另外第 4 课的检索要拿 knowledge_entries.body 分词，正文里混一个不可见字符也是数据缺陷。
	//
	// 剥掉 BOM 不影响 R4.4 的语义：带 BOM 的内容**本来就是**合法 UTF-8，
	// 拒绝它反而会把正常文件判成 400。这里做的是归一化，不是放宽校验。
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	// 剥完可能就空了 —— 「只有一个 BOM」的文件在 len(raw)==0 那一关是过不去的，
	// 必须在这里补一次，否则会落一份 0 字节的正文进库。
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "empty_content", "detail": "材料内容不能为空（去掉 UTF-8 BOM 后无内容）",
		})
		return
	}

	// ④ 标题：客户端文件名**只作展示标题**，剥掉目录部分与扩展名。
	//    它不参与任何路径构造——这是 R4.5 的一半。
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(hdr.Filename), filepath.Ext(hdr.Filename)))
	if title == "" {
		title = "未命名材料"
	}
	title = truncateRunes(title, maxTitleRunes)

	// ⑤ 存储名由服务端生成，与客户端文件名彻底无关（R4.5 的另一半）。
	stored, err := newStoredName(ext)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "material.stored_name_generate_failed", "生成存储名失败", "detail", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error"})
		return
	}

	// ⑥ 先写盘再入库：若入库失败则删掉文件（R4.6）。
	// 反过来「先入库再写盘」会留下指向不存在文件的孤儿记录，更难收尾。
	if err := os.MkdirAll(h.uploadDir, 0o700); err != nil {
		obslog.ErrorCtx(r.Context(), "material.upload_dir_failed", "创建上传目录失败", "detail", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error"})
		return
	}
	full := filepath.Join(h.uploadDir, stored)
	if err := writeFileExclusive(full, raw); err != nil {
		obslog.ErrorCtx(r.Context(), "material.file_write_failed", "写入材料文件失败", "stored_name", stored, "detail", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error"})
		return
	}

	// ⑦ 同事务写两表（D5）。失败即回滚，并删掉刚写的文件，不留孤儿。
	id, err := h.insertMaterialTx(sess, title, stored, int64(len(raw)), string(raw))
	if err != nil {
		if rmErr := os.Remove(full); rmErr != nil {
			obslog.ErrorCtx(r.Context(), "material.rollback_leftover", "回滚残留：删除已写文件失败", "stored_name", stored, "detail", rmErr.Error())
		}
		obslog.ErrorCtx(r.Context(), "material.insert_failed", "入库失败，已回滚并删除文件", "user", sess.Username, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
		return
	}

	obslog.InfoCtx(r.Context(), "material.uploaded", "教师上传成功", "user", sess.Username, "class_id", sess.ClassID, "material_id", id, "title", title, "bytes", len(raw))
	// ── ⑧ 检索索引（add-class-scoped-retrieval R4，独立于入库事务）──
	//
	// 边界（design D7）：材料与正文已合法入库（R4.6 阶段完成），
	// 其后的切分/嵌入/写向量失败**只影响索引状态**，不改变上传的成功语义：
	// 响应仍是 201，切片按 failed 落库，检索侧自动排除（R2.4）。
	if h.indexer != nil {
		opts := knowledge.Options{Strategy: strings.TrimSpace(r.FormValue("strategy"))}
		if v := strings.TrimSpace(r.FormValue("size")); v != "" {
			opts.Size, _ = strconv.Atoi(v)
		}
		if v := strings.TrimSpace(r.FormValue("overlap_percent")); v != "" {
			opts.OverlapPercent, _ = strconv.Atoi(v)
		}
		opts.StripLinks = r.FormValue("strip_links") == "true"
		opts.CollapseSpace = r.FormValue("collapse_space") == "true"
		if n, err := h.indexer.IndexMaterial(r.Context(), id, sess.ClassID, string(raw), opts); err != nil {
			obslog.ErrorCtx(r.Context(), "material.index_failed", "材料索引未建成，切片标 failed（上传语义不受影响）", "material_id", id, "detail", err.Error())
		} else {
			obslog.InfoCtx(r.Context(), "material.indexed", "材料索引完成", "material_id", id, "chunks", n)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         id,
		"title":      title,
		"class_id":   sess.ClassID,
		"class_name": sess.ClassName,
		"size_bytes": int64(len(raw)),
		"uploader":   sess.Username,
	})
}

// ── 上传辅助 ──────────────────────────────────────────────────

// maxTitleRunes 限制标题长度。materials.title 是 VARCHAR(255)（字符数），
// 按 rune 截断，避免把多字节字符切成半个。
const maxTitleRunes = 200

// insertMaterialTx 在**同一事务**内写 materials 与 knowledge_entries（R4.1、D5）。
//
// 为什么必须同事务：只写盘不入库 → 第 4 课检索没有数据；只入库不写盘 → 下载必然失败。
// 任一单独成功都是半成品，会把验收结果污染成「看起来能用」。
func (h *Handlers) insertMaterialTx(
	sess *auth.Session, title, stored string, size int64, body string,
) (int64, error) {
	tx, err := h.pool.Begin()
	if err != nil {
		return 0, fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Commit 成功后这里是 no-op

	res, err := tx.Exec(
		`INSERT INTO materials (class_id, title, stored_name, size_bytes, uploader_id)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.ClassID, title, stored, size, sess.UserID)
	if err != nil {
		return 0, fmt.Errorf("写入 materials: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("取 materials 自增 ID: %w", err)
	}

	// class_id 同样取会话班级：两表租户列必须一致，否则第 4 课检索会跨班。
	if _, err := tx.Exec(
		`INSERT INTO knowledge_entries (material_id, class_id, body) VALUES (?, ?, ?)`,
		id, sess.ClassID, body); err != nil {
		return 0, fmt.Errorf("写入 knowledge_entries: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交事务: %w", err)
	}
	return id, nil
}

// newStoredName 生成磁盘存储名：服务端随机，与客户端文件名完全无关（R4.5）。
//
// 用 crypto/rand 而非可预测的 ID：存储名一旦可枚举，就等于多一条「猜文件名」的
// 探测信道。文件本来只能经鉴权接口读出，少一条信道更好。
func newStoredName(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "m-" + hex.EncodeToString(b[:]) + ext, nil
}

// writeFileExclusive 以 O_EXCL 写入：撞名即报错，而不是静默覆盖既有材料。
func writeFileExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// truncateRunes 按字符（不是字节）截断，避免切坏多字节字符。
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// ── 内部 ──────────────────────────────────────────────────────

// materialRow 是 fetch-then-check 取回的原始行（含存储名，仅内部使用）。
type materialRow struct {
	ID         int64
	ClassID    int64
	ClassName  string
	Title      string
	SizeBytes  int64
	UploaderID int64
	Uploader   string
	CreatedAt  time.Time
	StoredName string
}

// loadGuarded 执行「取 ID → fetch → 核对班级」三步。
// 任一环节不通过（含跨班）都写 404 并返回 ok=false。
func (h *Handlers) loadGuarded(w http.ResponseWriter, r *http.Request) (*auth.Session, *materialRow, bool) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauthorized(w)
		return nil, nil, false
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		// ID 形态非法与 ID 不存在同形——否则 `abc` 与 `99999` 的差异本身是信息
		writeNotFound(w)
		return nil, nil, false
	}

	row, err := h.fetchByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return nil, nil, false
		}
		obslog.ErrorCtx(r.Context(), "material.read_failed", "按 ID 读取材料失败", "material_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "db_unavailable"})
		return nil, nil, false
	}

	if row.ClassID != sess.ClassID {
		// 跨班：**对外同形 404**，差异只落在服务端日志里（design.md D3）。
		obslog.ErrorCtx(r.Context(), "material.cross_class_denied", "跨班访问被拒", "user", sess.Username, "class_id", sess.ClassID, "material_id", row.ID, "material_class_id", row.ClassID)
		writeNotFound(w)
		return nil, nil, false
	}

	return sess, row, true
}

// fetchByID 按主键取行，**不带班级条件**（D4）。
func (h *Handlers) fetchByID(id int64) (*materialRow, error) {
	const q = `
		SELECT m.id, m.class_id, c.name, m.title, m.size_bytes,
		       m.uploader_id, u.username, m.created_at, m.stored_name
		FROM materials m
		JOIN classes c ON c.id = m.class_id
		JOIN users   u ON u.id = m.uploader_id
		WHERE m.id = ?`

	var row materialRow
	err := h.pool.QueryRow(q, id).Scan(
		&row.ID, &row.ClassID, &row.ClassName, &row.Title, &row.SizeBytes,
		&row.UploaderID, &row.Uploader, &row.CreatedAt, &row.StoredName,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// escapeLike 转义 LIKE 通配符，避免用户输入的 % 变成「匹配全部」。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// contentTypeFor 只对白名单内的两个扩展名给出类型，其余交给 ServeContent 推断。
func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return mime.TypeByExtension(filepath.Ext(name))
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(notFoundBody))
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

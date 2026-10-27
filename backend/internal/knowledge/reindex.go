package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// reindex 接口（tasks 5.3–5.4 / spec R4.4–R4.5）。
//
// POST /api/materials/{id}/reindex —— 教师专属；
// 按本次请求的策略重切：先删旧切片与旧向量再写入（与上传同一条 IndexMaterial 链路）。
// 请求体可带策略参数；缺省 auto。class_id 若出现，一律丢弃（沿用 R4.7 语义）。
//
// 隔离口径与材料详情一致：跨班与不存在**同形 404**（继承 auth-upload R5）。

type reindexRequest struct {
	Strategy       string `json:"strategy"`
	Size           int    `json:"size"`
	OverlapPercent int    `json:"overlap_percent"`
	StripLinks     bool   `json:"strip_links"`
	CollapseSpace  bool   `json:"collapse_space"`
	ClassID        any    `json:"class_id"`
}

// Reindex 处理 POST /api/materials/{id}/reindex。需要 RequireTeacher 包裹。
func (ix *Indexer) Reindex(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeNotFound(w)
		return
	}

	// fetch-then-check：先取行再核对班级（与材料详情同形）
	var classID int64
	var body string
	err = ix.pool.QueryRowContext(r.Context(),
		`SELECT m.class_id, ke.body
		 FROM materials m
		 JOIN knowledge_entries ke ON ke.material_id = m.id
		 WHERE m.id = ?`, id).Scan(&classID, &body)
	if err != nil {
		// 不存在 / 无正文 / 跨班——对外一律同形 404（差异只进日志）
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return
		}
		obslog.ErrorCtx(r.Context(), "index.reindex_material_read_failed", "重建索引时读取材料失败", "material_id", id, "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
		return
	}
	if classID != sess.ClassID {
		obslog.ErrorCtx(r.Context(), "index.reindex_cross_class_denied", "重建索引被拒：材料不属于本班", "user", sess.Username, "class_id", sess.ClassID, "material_id", id, "material_class_id", classID)
		writeNotFound(w)
		return
	}

	var req reindexRequest
	if r.Body != nil {
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		if err != nil && !errors.Is(err, io.EOF) { // 空 body 合法 = 缺省 auto
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
			return
		}
	}
	if req.ClassID != nil {
		obslog.InfoCtx(r.Context(), "index.reindex_class_id_ignored", "忽略重建索引请求里的 class_id", "client_class_id", req.ClassID)
	}

	opts := Options{
		Strategy:       req.Strategy,
		Size:           req.Size,
		OverlapPercent: req.OverlapPercent,
		StripLinks:     req.StripLinks,
		CollapseSpace:  req.CollapseSpace,
	}
	// 策略非法 → 400（含 custom 参数越界，R9.3）
	if _, err := opts.normalize(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}

	n, err := ix.IndexMaterial(r.Context(), id, classID, body, opts)
	if err != nil {
		obslog.ErrorCtx(r.Context(), "index.reindex_failed", "重建索引失败", "material_id", id, "detail", err.Error())
		// 材料与正文仍在；切片状态已按失败语义落库。对外 503 指明索引未建成。
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "reindex_failed", "detail": "索引未建成，切片已标记失败",
		})
		return
	}
	obslog.InfoCtx(r.Context(), "index.reindex_done", "教师重建索引成功", "user", sess.Username, "material_id", id, "strategy", opts.Strategy, "chunks", n)
	writeJSON(w, http.StatusOK, map[string]any{
		"material_id": id,
		"strategy":    opts.Strategy,
		"chunks":      n,
	})
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not_found"}`))
}

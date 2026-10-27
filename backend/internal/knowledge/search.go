package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// 检索接口（tasks 6.x / spec R1、R2、R5）。
//
// 隔离三重防线（design D6）：
//   1. 关键字路 SQL 带 class_id = 会话班级；
//   2. 向量路 Qdrant filter 带 class_id；
//   3. 向量路拿到主键后回 MySQL **再用同一班级条件**取正文。
// 请求体里的 class_id 读出来只记日志、随即丢弃（沿用 auth-upload R4.7「忽略」语义）。

const vectorScoreThreshold = 0.35 // R5.2：低于阈值的候选丢弃
const rrfK = 60                   // R5.4：RRF 常数

// SearchDeps 汇集检索依赖。
type SearchDeps struct {
	Pool *sql.DB
	Emb  *Embedder
	Qd   *Qdrant
}

// searchRequest 是 /api/search 的请求体。
type searchRequest struct {
	Query   string `json:"query"`
	Mode    string `json:"mode"`
	TopK    int    `json:"top_k"`
	ClassID any    `json:"class_id"` // 刻意收下：只为日志，不参与任何逻辑（R2.2）
}

// Hit 是溯源块（design D5 / spec R3）。
//
// 第 5 课起分数拆三档（官方 contract OpSRCH001Response）：
//   - score_final：融合后的最终分（keyword 路 = 全文相关度；hybrid = RRF 融合分）；
//   - score_keyword：关键字路原始分（未命中该路时为 0）；
//   - score_vector：向量路余弦分（未命中该路时为 null）。
//
// `score` 保留为 score_final 的兼容别名 —— 第 4 课的 verify-retrieval.sh 仍断言它，
// 移除会破坏既有验收。待第 5 课验收通过后另行清理。
type Hit struct {
	MaterialID    int64    `json:"material_id"`
	MaterialTitle string   `json:"material_title"`
	ChunkID       int64    `json:"chunk_id"`
	ChunkIndex    int      `json:"chunk_index"`
	Snippet       string   `json:"snippet"`
	StartOffset   int      `json:"start_offset"`
	EndOffset     int      `json:"end_offset"`
	Score         float64  `json:"score"` // 兼容别名 = ScoreFinal
	ScoreFinal    float64  `json:"score_final"`
	ScoreKeyword  float64  `json:"score_keyword"`
	ScoreVector   *float64 `json:"score_vector"` // 指针：区分"0 分"与"该路未命中（null）"
}

type chunkRow struct {
	ID         int64
	MaterialID int64
	ChunkIndex int
	Text       string
	Start      int
	End        int
	Score      float64
	Title      string
}

// Search 处理 POST /api/search。
func (d *SearchDeps) Search(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}

	// R1.2：query 非空
	query := strings.TrimSpace(req.Query)
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "query 不能为空"})
		return
	}
	// R1.3：mode 三值，缺省 hybrid（R1.5）
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "hybrid"
	}
	if mode != "keyword" && mode != "vector" && mode != "hybrid" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "mode 必须是 keyword/vector/hybrid"})
		return
	}
	// R1.4：top_k ∈ [1,20]，缺省 5
	topK := req.TopK
	if topK == 0 {
		topK = 5
	}
	if topK < 1 || topK > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "top_k 必须在 [1,20]"})
		return
	}
	// R2.2：请求里的 class_id 一律丢弃，只记日志
	if req.ClassID != nil {
		obslog.InfoCtx(r.Context(), "search.class_id_ignored", "忽略检索请求里的 class_id，范围固定为会话班级", "client_class_id", req.ClassID, "user", sess.Username, "class_id", sess.ClassID)
	}

	ctx := r.Context()
	var hits []Hit
	var err error

	switch mode {
	case "keyword":
		hits, err = d.keywordPath(ctx, sess.ClassID, query, topK, nil)
		if err != nil {
			obslog.ErrorCtx(r.Context(), "search.keyword_failed", "关键字检索路失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "db_unavailable"})
			return
		}
	case "vector":
		hits, err = d.vectorPath(ctx, sess.ClassID, query, topK, nil)
		if err != nil {
			// R7.1：嵌入网关或 Qdrant 不可达 → 503，hits 空、不含分数
			obslog.ErrorCtx(r.Context(), "search.vector_failed", "向量检索路失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "retrieval_unavailable"})
			return
		}
	case "hybrid":
		hits, err = d.HybridPath(ctx, sess.ClassID, query, topK)
		if err != nil {
			obslog.ErrorCtx(r.Context(), "search.hybrid_failed", "混合检索路失败", "detail", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "retrieval_unavailable"})
			return
		}
	}
	if hits == nil {
		hits = []Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "hits": hits})
}

const chunkSelect = `
	SELECT kc.id, kc.material_id, kc.chunk_index, kc.chunk_text, kc.start_offset, kc.end_offset, m.title`

// keywordPath：MySQL 全文索引路（R5.1 / design D3）。
// 不调用嵌入网关——整条路径只碰 MySQL。
//
// matIDs 非空时只在给定材料内检索（第 5 课：智能体绑定的材料）；空表示不限材料。
func (d *SearchDeps) keywordPath(ctx context.Context, classID int64, query string, topK int, matIDs []int64) ([]Hit, error) {
	clause, extra := materialClause(matIDs)
	args := []any{query, classID}
	args = append(args, extra...)
	args = append(args, query, topK)
	rows, err := d.Pool.QueryContext(ctx, chunkSelect+`,
		MATCH(kc.chunk_text) AGAINST(? IN NATURAL LANGUAGE MODE) AS score
		FROM knowledge_chunks kc
		JOIN materials m ON m.id = kc.material_id
		WHERE kc.class_id = ? AND kc.index_status = 'ready'`+clause+`
		  AND MATCH(kc.chunk_text) AGAINST(? IN NATURAL LANGUAGE MODE)
		ORDER BY score DESC, kc.id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHits(rows)
}

// vectorPath：问句嵌入 → Qdrant（class filter）→ 阈值过滤 → 回表再核对班级（R5.2/R5.3/R2.5）。
//
// matIDs 非空时同时限制在给定材料内（Qdrant filter + 回表 SQL 双重收束）。
func (d *SearchDeps) vectorPath(ctx context.Context, classID int64, query string, topK int, matIDs []int64) ([]Hit, error) {
	vectors, err := d.Emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("问句嵌入失败: %w", err)
	}
	qhits, err := d.Qd.Search(ctx, vectors[0], classID, matIDs, topK)
	if err != nil {
		return nil, fmt.Errorf("qdrant 检索失败: %w", err)
	}

	// R5.3：丢弃余弦 < 0.35
	kept := make([]QdrantHit, 0, len(qhits))
	for _, h := range qhits {
		if h.Score >= vectorScoreThreshold {
			kept = append(kept, h)
		}
	}
	if len(kept) == 0 {
		return []Hit{}, nil
	}

	// R2.5：回表核对——主键 + 班级 + ready 三条件缺一不可（第 5 课再加材料范围）
	clause, extra := materialClause(matIDs)
	args := append([]any{classID}, chunkIDs(kept)...)
	args = append(args, extra...)
	rows, err := d.Pool.QueryContext(ctx, chunkSelect+`, 0 AS score
		FROM knowledge_chunks kc
		JOIN materials m ON m.id = kc.material_id
		WHERE kc.class_id = ? AND kc.index_status = 'ready' AND kc.id IN (`+placeholders(len(kept))+`)`+clause,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID, err := scanChunkRows(rows)
	if err != nil {
		return nil, err
	}

	// 保持 Qdrant 名次顺序
	out := make([]Hit, 0, len(kept))
	for _, qh := range kept {
		row, ok := byID[qh.ChunkID]
		if !ok {
			continue // 回表被班级/ready/材料条件挡掉（R2.5 的构造用例就落在这里）
		}
		row.Score = round4(qh.Score)
		h := toHit(row)
		// 向量路：ScoreKeyword 归 0（该路未提供关键字分），向量分填 ScoreVector
		sv := round4(qh.Score)
		h.ScoreKeyword = 0
		h.ScoreVector = &sv
		h.ScoreFinal = sv
		h.Score = sv
		out = append(out, h)
	}
	return out, nil
}

// HybridPath：两路各取 topK，按名次 RRF(k=60) 融合（R5.4/R5.5）。不限材料。
// 任一路的**依赖**失败（DB 除外）都会导致整体 503（R7.1：缺席指「无命中」，不是「依赖挂了」）。
//
// ⚠️ 这里不能直接转调 HybridPathIn：那个函数把「matIDs 为空」解释成
// 「智能体没绑材料 → 检索为空」，而本函数的不限材料语义恰好也是空。
// 2026-10-27 踩过：两者混用导致 /api/search 与 /api/ask 的 hybrid 恒返回 0 条，
// 而 keyword / vector 两路都正常，e2e-tutor 的引用芯片断言因此整条失败。
func (d *SearchDeps) HybridPath(ctx context.Context, classID int64, query string, topK int) ([]Hit, error) {
	return d.hybridFuse(ctx, classID, query, topK, nil)
}

// HybridPathIn：HybridPath 的「限定材料」版本。
//
// 第 5 课：检索只在会话所选智能体绑定的材料内进行。matIDs 为空即「该智能体没绑材料」，
// 按课件口径此时检索为空（不报错、不越界查全班）。
func (d *SearchDeps) HybridPathIn(ctx context.Context, classID int64, query string, topK int, matIDs []int64) ([]Hit, error) {
	if len(matIDs) == 0 {
		return []Hit{}, nil
	}
	return d.hybridFuse(ctx, classID, query, topK, matIDs)
}

// hybridFuse：真正干活的融合实现。matIDs 为 nil 表示不过滤材料。
// 这个参数只能由上面两个入口决定，别再让「空」同时承担两种意思。
func (d *SearchDeps) hybridFuse(ctx context.Context, classID int64, query string, topK int, matIDs []int64) ([]Hit, error) {
	kw, kwErr := d.keywordPath(ctx, classID, query, topK, matIDs)
	if kwErr != nil {
		return nil, fmt.Errorf("关键字路失败: %w", kwErr)
	}
	vec, vecErr := d.vectorPath(ctx, classID, query, topK, matIDs)
	if vecErr != nil {
		return nil, fmt.Errorf("向量路失败: %w", vecErr)
	}
	return fuseRRF(kw, vec, topK), nil
}

// fuseRRF：把两路结果按 RRF（k=60）融合成一路（R5.4/R5.5/R5.6）。
//
// 抽成纯函数是为了能被单测覆盖：融合规则（同名 chunk 合分、并列时 chunk_id 升序、
// topK 截断）以前只藏在 HTTP 路径里，一条测试都没有，改坏也不会红。
func fuseRRF(kw, vec []Hit, topK int) []Hit {
	// RRF：每路按（score 降序、chunk_id 升序）排名，贡献 1/(k+rank)
	rrf := map[int64]float64{}
	for _, path := range [][]Hit{kw, vec} {
		for rank, h := range path {
			rrf[h.ChunkID] += 1.0 / float64(rrfK+rank+1)
		}
	}
	// 合并两路的行内容，并保留各自的原始分：
	//   keyword 路带 ScoreKeyword、无 ScoreVector；
	//   vector 路带 ScoreVector、无 ScoreKeyword。
	// 同名 chunk 在两路都出现时，把两路的分都并到同一条上。
	byID := map[int64]Hit{}
	for _, h := range kw {
		byID[h.ChunkID] = h
	}
	for _, h := range vec {
		if merged, ok := byID[h.ChunkID]; ok {
			merged.ScoreVector = h.ScoreVector
			byID[h.ChunkID] = merged
		} else {
			byID[h.ChunkID] = h
		}
	}

	out := make([]Hit, 0, len(rrf))
	for id, score := range rrf {
		h := byID[id]
		f := round4(score)
		h.Score = f
		h.ScoreFinal = f
		out = append(out, h)
	}
	// R5.6：score 降序、并列 chunk_id 升序
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

// ── 扫描与辅助 ────────────────────────────────────────────────

func scanHits(rows *sql.Rows) ([]Hit, error) {
	list := []Hit{}
	for rows.Next() {
		var row chunkRow
		if err := rows.Scan(&row.ID, &row.MaterialID, &row.ChunkIndex, &row.Text,
			&row.Start, &row.End, &row.Title, &row.Score); err != nil {
			return nil, err
		}
		list = append(list, toHit(row))
	}
	return list, rows.Err()
}

func scanChunkRows(rows *sql.Rows) (map[int64]chunkRow, error) {
	out := map[int64]chunkRow{}
	for rows.Next() {
		var row chunkRow
		if err := rows.Scan(&row.ID, &row.MaterialID, &row.ChunkIndex, &row.Text,
			&row.Start, &row.End, &row.Title, &row.Score); err != nil {
			return nil, err
		}
		out[row.ID] = row
	}
	return out, rows.Err()
}

// toHit 组装溯源块。snippet = 切片全文：切片区间 [start,end) 是源文本的
// 连续子串，所以「snippet == 按 rune 取区间」恒成立（R3.2 无预处理时）。
//
// 分数：row.Score 是**该路**的原始分。这里把它填进 ScoreFinal + ScoreKeyword，
// 向量路随后覆盖 ScoreVector 并重算 ScoreFinal（见 vectorPath / HybridPath）。
func toHit(row chunkRow) Hit {
	s := round4(row.Score)
	return Hit{
		MaterialID:    row.MaterialID,
		MaterialTitle: row.Title,
		ChunkID:       row.ID,
		ChunkIndex:    row.ChunkIndex,
		Snippet:       row.Text,
		StartOffset:   row.Start,
		EndOffset:     row.End,
		Score:         s,
		ScoreFinal:    s,
		ScoreKeyword:  s,
	}
}

func chunkIDs(hs []QdrantHit) []any {
	out := make([]any, len(hs))
	for i, h := range hs {
		out[i] = h.ChunkID
	}
	return out
}

func placeholders(n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = "?"
	}
	return strings.Join(ps, ",")
}

// materialClause 为「限定材料」生成 SQL 片段（` AND kc.material_id IN (?,?,...)`）与对应实参。
// ids 为空返回空串与 nil —— 调用方据此表示「不限材料」。
func materialClause(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, len(ids))
	ps := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		ps[i] = "?"
	}
	return " AND kc.material_id IN (" + strings.Join(ps, ",") + ")", args
}

// materialInt64s 把 []int64 转成 []any（Qdrant filter 用）。
func materialInt64s(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func round4(f float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 4, 64), 64)
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

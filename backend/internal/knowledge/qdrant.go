package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Qdrant REST 客户端（tasks 4.2 / spec R4、R2.5）。
//
// 纪律：
//   - 点 ID = knowledge_chunks.id（R4.1 主键约定）；
//   - payload 只放五个标识字段，**绝不含正文**（R4.1 / design D2）；
//   - 一切查询必须带 class_id filter（R2.5）——本客户端把它写死在 Search 里。
type Qdrant struct {
	url        string // 已去尾部斜杠
	collection string
	dim        int
	http       *http.Client
}

// NewQdrant 构造。
func NewQdrant(url, collection string, dim int) *Qdrant {
	return &Qdrant{
		url:        url,
		collection: collection,
		dim:        dim,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
}

// ErrDimMismatch 表示集合已存在但向量维度与 EMBEDDING_DIM 不一致（R7.4）。
var ErrDimMismatch = errors.New("qdrant 集合向量维度与 EMBEDDING_DIM 不一致")

// Point 是待写入的一个点。
type Point struct {
	ID      int64          `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// do 执行一次 REST 调用并把响应体解析进 out（out 可为 nil）。
func (q *Qdrant) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.url+path, rd)
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := q.http.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant 不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("qdrant 返回 HTTP %d: %s", res.StatusCode, truncateForLog(string(raw), 200))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return nil
}

type qdrantCollectionInfo struct {
	Result struct {
		Config struct {
			Params struct {
				Vectors struct {
					Size     int    `json:"size"`
					Distance string `json:"distance"`
				} `json:"vectors"`
			} `json:"params"`
		} `json:"config"`
	} `json:"result"`
}

// EnsureCollection 幂等建集合（余弦，size=dim）。
// 已存在且维度不一致 → ErrDimMismatch（R7.4 / tasks 1.3 的校验入口）。
func (q *Qdrant) EnsureCollection(ctx context.Context) error {
	var info qdrantCollectionInfo
	err := q.do(ctx, http.MethodGet, "/collections/"+q.collection, nil, &info)
	if err == nil {
		got := info.Result.Config.Params.Vectors.Size
		if got != q.dim {
			return fmt.Errorf("%w: 集合 %q size=%d，EMBEDDING_DIM=%d",
				ErrDimMismatch, q.collection, got, q.dim)
		}
		return nil
	}
	// 404 = 集合不存在 → 创建；其他错误原样返回（含不可达）
	if !strings.Contains(err.Error(), "HTTP 404") {
		return err
	}
	create := map[string]any{
		"vectors": map[string]any{"size": q.dim, "distance": "Cosine"},
	}
	return q.do(ctx, http.MethodPut, "/collections/"+q.collection, create, nil)
}

// DeleteByMaterial 删除某材料的全部点（reindex 前置 / R4.4）。
func (q *Qdrant) DeleteByMaterial(ctx context.Context, materialID int64) error {
	body := map[string]any{
		"filter": map[string]any{
			"must": []any{
				map[string]any{"key": "material_id", "match": map[string]any{"value": materialID}},
			},
		},
	}
	return q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/delete", body, nil)
}

// Upsert 批量写点。wait=true 保证请求返回时已可检索（失败语义可判定）。
func (q *Qdrant) Upsert(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	body := map[string]any{"points": points}
	return q.do(ctx, http.MethodPut, "/collections/"+q.collection+"/points?wait=true", body, nil)
}

type qdrantSearchResponse struct {
	Result []struct {
		ID    float64 `json:"id"` // qdrant 以数字返回点 ID
		Score float64 `json:"score"`
	} `json:"result"`
}

// Search 按向量检索，**强制带 class_id filter**（R2.5）。返回 (chunkID, score) 列表。
//
// matIDs 非空时再叠加 material_id 过滤（第 5 课：只在智能体绑定的材料内检索）。
// 需要 import "strings" 之外的依赖；materialInt64s 在 search.go 中定义。
func (q *Qdrant) Search(ctx context.Context, vector []float32, classID int64, matIDs []int64, limit int) ([]QdrantHit, error) {
	must := []any{
		map[string]any{"key": "class_id", "match": map[string]any{"value": classID}},
	}
	if len(matIDs) > 0 {
		must = append(must, map[string]any{
			"key":   "material_id",
			"match": map[string]any{"any": materialInt64s(matIDs)},
		})
	}
	body := map[string]any{
		"vector":       vector,
		"limit":        limit,
		"with_payload": false,
		"filter":       map[string]any{"must": must},
	}
	var out qdrantSearchResponse
	if err := q.do(ctx, http.MethodPost, "/collections/"+q.collection+"/points/search", body, &out); err != nil {
		return nil, err
	}
	hits := make([]QdrantHit, 0, len(out.Result))
	for _, r := range out.Result {
		hits = append(hits, QdrantHit{ChunkID: int64(r.ID), Score: r.Score})
	}
	return hits, nil
}

// QdrantHit 是向量路的一次命中。
type QdrantHit struct {
	ChunkID int64
	Score   float64
}

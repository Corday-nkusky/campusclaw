package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 覆盖 tasks 4.3 / 4.4：契约级单测（httptest 假 Qdrant / 假网关）。
// 只证明「发出的请求长什么样」，不证明真库行为（design D12 纪律 2）。

func TestQdrantUpsertContract(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{},"status":"ok"}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "campusclaw_chunks", 4)
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	err := q.Upsert(context.Background(), []Point{
		{ID: 42, Vector: vec, Payload: map[string]any{
			"class_id": 1, "material_id": 2, "knowledge_entry_id": 3, "chunk_id": 42, "chunk_index": 0,
		}},
	})
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/collections/campusclaw_chunks/points" {
		t.Fatalf("请求形态不对: %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotQuery, "wait=true") {
		t.Fatal("upsert 必须带 wait=true（失败语义可判定）")
	}
	points, _ := gotBody["points"].([]any)
	if len(points) != 1 {
		t.Fatalf("应恰好 1 个点，实际 %d", len(points))
	}
	p := points[0].(map[string]any)
	if p["id"].(float64) != 42 {
		t.Fatalf("点 ID 必须等于 chunk 主键 42，实际 %v", p["id"])
	}
	payload := p["payload"].(map[string]any)
	allowed := map[string]bool{"class_id": true, "material_id": true, "knowledge_entry_id": true, "chunk_id": true, "chunk_index": true}
	for k := range payload {
		if !allowed[k] {
			t.Fatalf("payload 出现多余字段 %q", k)
		}
	}
	// 正文绝不能进 payload（R4.1）
	if strings.Contains(mustJSON(t, gotBody), "正文") || strings.Contains(mustJSON(t, gotBody), "chunk_text") {
		t.Fatal("payload 不得包含正文")
	}
}

func TestQdrantSearchCarriesClassFilter(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"result":[{"id":7,"score":0.83},{"id":9,"score":0.41}]}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "campusclaw_chunks", 4)
	hits, err := q.Search(context.Background(), []float32{1, 0, 0, 0}, 2, nil, 10)
	if err != nil {
		t.Fatalf("search 失败: %v", err)
	}
	filter := gotBody["filter"].(map[string]any)
	must := filter["must"].([]any)
	found := false
	for _, m := range must {
		cond := m.(map[string]any)
		if cond["key"] == "class_id" {
			found = true
			v := cond["match"].(map[string]any)["value"].(float64)
			if v != 2 {
				t.Fatalf("class_id filter 值=%v，期望 2", v)
			}
		}
	}
	if !found {
		t.Fatal("search 请求必须带 class_id filter（R2.5）")
	}
	if len(hits) != 2 || hits[0].ChunkID != 7 || hits[0].Score != 0.83 {
		t.Fatalf("命中解析不对: %+v", hits)
	}
}

// 第 5 课：检索可限定在智能体绑定的材料内 → material_id filter 必须下发。
func TestQdrantSearchCarriesMaterialFilter(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "campusclaw_chunks", 4)
	if _, err := q.Search(context.Background(), []float32{1, 0, 0, 0}, 2, []int64{3, 5}, 10); err != nil {
		t.Fatalf("search 失败: %v", err)
	}
	must := gotBody["filter"].(map[string]any)["must"].([]any)
	found := false
	for _, m := range must {
		cond := m.(map[string]any)
		if cond["key"] == "material_id" {
			found = true
			anyv, ok := cond["match"].(map[string]any)["any"].([]any)
			if !ok || len(anyv) != 2 {
				t.Fatalf("material_id filter 形态不对: %v", cond["match"])
			}
		}
	}
	if !found {
		t.Fatal("限定材料时应带 material_id filter")
	}
}

func TestQdrantEnsureCollectionDimMismatch(t *testing.T) {
	// 已存在的集合 size=8，EMBEDDING_DIM=4 → ErrDimMismatch（R7.4 / tasks 1.3）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"config":{"params":{"vectors":{"size":8,"distance":"Cosine"}}}}}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "campusclaw_chunks", 4)
	err := q.EnsureCollection(context.Background())
	if !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("应返回 ErrDimMismatch，实际: %v", err)
	}
}

func TestQdrantEnsureCollectionCreates(t *testing.T) {
	var created bool
	var createdBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/collections/campusclaw_chunks"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"not_found"}`))
		case r.Method == http.MethodPut:
			created = true
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &createdBody)
			_, _ = w.Write([]byte(`{"result":{},"status":"ok"}`))
		}
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "campusclaw_chunks", 1024)
	if err := q.EnsureCollection(context.Background()); err != nil {
		t.Fatalf("建集合失败: %v", err)
	}
	if !created {
		t.Fatal("404 后必须创建集合")
	}
	vec := createdBody["vectors"].(map[string]any)
	if vec["size"].(float64) != 1024 || vec["distance"] != "Cosine" {
		t.Fatalf("集合参数不对: %v", createdBody)
	}
}

func TestEmbedderContract(t *testing.T) {
	var gotModel string
	var gotInput []any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("路径应为 /v1/embeddings，实际 %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		gotModel, _ = req["model"].(string)
		gotInput, _ = req["input"].([]any)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4]},{"index":1,"embedding":[0.4,0.3,0.2,0.1]}]}`))
	}))
	defer srv.Close()

	e := NewEmbedder(srv.URL, "sk-secret", "test-model", 4)
	vecs, err := e.Embed(context.Background(), []string{"甲", "乙"})
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Fatalf("Authorization 头不对: %q", gotAuth)
	}
	if gotModel != "test-model" || len(gotInput) != 2 {
		t.Fatalf("请求体不对: model=%q input=%v", gotModel, gotInput)
	}
	if len(vecs) != 2 || len(vecs[0]) != 4 || vecs[1][0] != 0.4 {
		t.Fatalf("向量解析不对: %+v", vecs)
	}
}

func TestEmbedderDimMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)) // 维度 2 ≠ 4
	}))
	defer srv.Close()

	e := NewEmbedder(srv.URL, "k", "m", 4)
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "维度不一致") {
		t.Fatalf("维度不一致必须报错，实际: %v", err)
	}
}

func TestEmbedderNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	e := NewEmbedder(srv.URL, "k", "m", 4)
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("非 2xx 必须归一为错误，实际: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Embedder：OpenAI 兼容 /v1/embeddings 客户端（tasks 4.1 / spec R4）。
//
// 错误纪律：超时与非 2xx 归一为可判定的 error（调用方据此把切片标 failed
// 或对检索返回 503），绝不把半成品数据往下传。
type Embedder struct {
	base  string // 已去尾部斜杠，如 https://api.example.com
	key   string
	model string
	dim   int
	http  *http.Client
}

// NewEmbedder 构造。dim 用于校验网关返回的向量维度（R7.4 的前置）。
func NewEmbedder(base, key, model string, dim int) *Embedder {
	return &Embedder{
		base:  base,
		key:   key,
		model: model,
		dim:   dim,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Dim 返回期望向量维度。
func (e *Embedder) Dim() int { return e.dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed 批量嵌入。返回向量顺序与输入一致。
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("序列化嵌入请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造嵌入请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}

	res, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("嵌入网关不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取嵌入响应失败: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("嵌入网关返回 HTTP %d: %s", res.StatusCode, truncateForLog(string(raw), 200))
	}

	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析嵌入响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("嵌入网关报错: %s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("嵌入网关返回 %d 条，期望 %d 条", len(out.Data), len(texts))
	}

	vectors := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			return nil, fmt.Errorf("嵌入网关返回越界 index=%d", d.Index)
		}
		if len(d.Embedding) != e.dim {
			return nil, fmt.Errorf("向量维度不一致：网关返回 %d，EMBEDDING_DIM=%d", len(d.Embedding), e.dim)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("嵌入网关缺少 index=%d 的向量", i)
		}
	}
	return vectors, nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// 问答接口（tasks 7.x / spec R6 / design D8）。
//
// 红线：
//   - /api/ask 不是第三种检索——固定用混合模式取本班前 4 条；
//   - 无命中：固定文案 + citations 空 + **不调用对话网关**（R6.1）；
//   - 送网关的只有标题/序号/正文/提问，无向量分量、无他班材料（R6.3）；
//   - system 由服务端写入，客户端注入的 system 一律丢弃（R6.5）；
//   - 非流式（R6.7，流式在第 5 课）。

const askTopK = 4
const notFoundAnswer = "资料中未找到相关内容"

// ChatClient：OpenAI 兼容 /v1/chat/completions 客户端（非流式）。
type ChatClient struct {
	base  string
	key   string
	model string
	http  *http.Client
	// Dump 非空时，每次请求都会把**逐字请求体**交给它（核验/取证用）。
	// 生产路径不设，等于零开销。
	Dump func(body []byte)
}

// NewChatClient 构造。
func NewChatClient(base, key, model string) *ChatClient {
	return &ChatClient{base: base, key: key, model: model, http: &http.Client{Timeout: 60 * time.Second}}
}

// ChatMessage 是一条对话消息。
//
// 后三个字段只在工具调用链路里出现（第 6 课）：assistant 消息带 tool_calls，
// 工具结果以 role="tool" + tool_call_id 回填。普通问答时它们都留空、不进报文。
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall 是模型要求执行的一次工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 是调用目标与参数（arguments 是**字符串形态的 JSON**，不是对象）。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSpec 是请求里 tools 数组的一项（OpenAI 兼容的 function 形态）。
type ToolSpec struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

// FunctionSpec 是工具自述：名字、说明、参数 JSON Schema。
type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Stream     bool          `json:"stream"`
	Tools      []ToolSpec    `json:"tools,omitempty"`
	ToolChoice any           `json:"tool_choice,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

// Complete 调对话网关，返回回答文本。非 2xx/超时归一为可判定错误（R7.3）。
func (c *ChatClient) Complete(ctx context.Context, messages []ChatMessage) (string, error) {
	body, err := json.Marshal(chatRequest{Model: c.model, Messages: messages, Stream: false})
	if err != nil {
		return "", fmt.Errorf("序列化对话请求: %w", err)
	}
	c.dump(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造对话请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("对话网关不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("读取对话响应失败: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("对话网关返回 HTTP %d: %s", res.StatusCode, truncateForLog(string(raw), 200))
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析对话响应失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("对话网关未返回任何 choices")
	}
	return out.Choices[0].Message.Content, nil
}

// serverSystem 是服务端写死的 system（R6.5：客户端的 system 到不了网关）。
const serverSystem = "你是一个班级知识库问答助手。只能依据用户消息中给出的带编号资料回答问题；" +
	"引用资料时用 [n] 标注对应编号；资料中没有的信息不要编造。"

// AskDeps 汇集问答依赖。
type AskDeps struct {
	Pool *sql.DB
	Srch *SearchDeps
	Chat *ChatClient
}

type askRequest struct {
	Question string `json:"question"`
	System   string `json:"system"` // 收下即丢弃（R6.5）
	ClassID  any    `json:"class_id"`
}

// Ask 处理 POST /api/ask。
func (d *AskDeps) Ask(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": "question 不能为空"})
		return
	}
	if req.System != "" {
		obslog.InfoCtx(r.Context(), "ask.system_dropped", "丢弃客户端注入的 system", "chars", len(req.System), "user", sess.Username)
	}
	if req.ClassID != nil {
		obslog.InfoCtx(r.Context(), "ask.class_id_ignored", "忽略问答请求里的 class_id，范围固定为会话班级", "client_class_id", req.ClassID, "user", sess.Username, "class_id", sess.ClassID)
	}

	// R6.2：混合模式、本班、前 4 条
	hits, err := d.Srch.HybridPath(r.Context(), sess.ClassID, strings.TrimSpace(req.Question), askTopK)
	if err != nil {
		// R7.1：检索依赖（Qdrant/嵌入）不可用 → 503
		obslog.ErrorCtx(r.Context(), "ask.retrieval_failed", "问答前置检索失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "retrieval_unavailable"})
		return
	}

	// R6.1：无命中 → 固定文案，不调网关
	if len(hits) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":    notFoundAnswer,
			"citations": []Hit{},
		})
		return
	}

	// R6.3：送网关的内容 = 标题 + 序号 + 正文 + 提问
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] 材料《%s》切片 #%d：\n%s\n\n", i+1, h.MaterialTitle, h.ChunkIndex, h.Snippet)
	}
	b.WriteString("问题：" + strings.TrimSpace(req.Question))

	messages := []ChatMessage{
		{Role: "system", Content: serverSystem},
		{Role: "user", Content: b.String()},
	}
	answer, err := d.Chat.Complete(r.Context(), messages)
	if err != nil {
		// R7.3：对话网关不可达 → 503、citations 空、不返回部分回答
		obslog.ErrorCtx(r.Context(), "ask.gateway_failed", "对话网关调用失败", "detail", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "chat_unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":    sanitizeCitations(answer, len(hits)),
		"citations": hits,
	})
}

// citationRe 匹配 [n] 形式的引用标注。
var citationRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// sanitizeCitations 保证回答中出现的 [n] 都落在 1..n（R6.4）：越界引用剥掉括号。
// 依据文本不变——只移除失效指针，不用假出处顶替。
func sanitizeCitations(answer string, n int) string {
	return citationRe.ReplaceAllStringFunc(answer, func(m string) string {
		num := 0
		fmt.Sscanf(m, "[%d]", &num)
		if num >= 1 && num <= n {
			return m
		}
		return m[1 : len(m)-1] // 剥掉方括号：保留文字，撤销引用
	})
}

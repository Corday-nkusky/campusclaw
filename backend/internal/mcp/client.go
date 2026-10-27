// Package mcp 承载「外部工具接入」（MCP，第 6 课 MCP-001~008）。
//
// 分工（课件 bridge/audit 页的硬结论）：
//
//	模型只**决定**要不要调工具、拿什么参数调；实际连接、执行、把结果回填给模型、记账，
//	全部由宿主（本包 + conversation 包）完成。
//
// 传输按第 6 课口径取 **Streamable HTTP**：一个 POST 端点（base_url + "/mcp"），
// 报文是 JSON-RPC 2.0。响应既可能是 application/json，也可能是 text/event-stream，
// 两种都要能解析——真实服务器两种都会出现。
package mcp

import (
	"bufio"
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

// protocolVersion 取第 6 课示范服务器同款（2024-11-05）。
const protocolVersion = "2024-11-05"

// JSONRPC 报文。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// ToolDef 是 tools/list 返回的一项工具定义（input_schema 原样保留）。
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client 是打向某一台 MCP 服务器的客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	session string // Mcp-Session-Id，服务器给了就带回
	nextID  int64
}

// NewClient 构造；超时按"工具调用可能慢"给到 20 秒。
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

// endpoint 返回 MCP 端点：约定 base_url 写服务器根，端点固定为 /mcp。
func (c *Client) endpoint() string {
	if strings.HasSuffix(c.BaseURL, "/mcp") {
		return c.BaseURL
	}
	return c.BaseURL + "/mcp"
}

// call 发一条 JSON-RPC 请求并解析结果。
//
// 兼容两种响应形态：
//   - Content-Type: application/json → 直接解一条报文；
//   - Content-Type: text/event-stream → 逐行读 data:，取第一条带 result/error 的报文。
//
// 空体（通知类请求的 202）按成功处理，返回 nil。
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	reqBody, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("序列化 JSON-RPC 请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("构造 MCP 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// 令牌只在这里出现原文，不进日志、不进响应体。
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MCP 服务器不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		c.session = sid
	}

	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 响应: %w", err)
	}
	if res.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(raw)) == 0 {
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("MCP 服务器返回 HTTP %d", res.StatusCode)
		}
		return nil, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP 服务器返回 HTTP %d: %s", res.StatusCode, clip(string(raw), 200))
	}

	payload := raw
	if strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		payload, err = firstSSEData(raw)
		if err != nil {
			return nil, err
		}
	}

	var out rpcResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("解析 MCP 响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("MCP 错误 %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

// Initialize 走握手：initialize + notifications/initialized。
//
// initialize 失败就是**连接失败**，调用方按契约回 502。
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "campusclaw", "version": "1.0.0"},
	})
	if err != nil {
		return err
	}
	// 通知没有 id、也没有结果；失败不影响后续 tools/list（服务器可能未实现）
	_, _ = c.call(ctx, "notifications/initialized", map[string]any{})
	return nil
}

// ListTools 拉工具清单。
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	res, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("解析 tools/list 失败: %w", err)
	}
	for i := range out.Tools {
		if len(out.Tools[i].InputSchema) == 0 {
			out.Tools[i].InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
	}
	return out.Tools, nil
}

// CallTool 执行一次工具调用，把 content 里的文本拼起来返回。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		// 有些服务器直接回裸字符串/对象，原样转成文本比报错更有用
		return string(res), nil
	}
	var b strings.Builder
	for _, part := range out.Content {
		if part.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
	}
	text := b.String()
	if out.IsError {
		if text == "" {
			text = "工具返回错误"
		}
		return "", errors.New(text)
	}
	if text == "" {
		text = string(res)
	}
	return text, nil
}

// firstSSEData 从 SSE 流里取第一条含 result 或 error 的 data 报文。
func firstSSEData(raw []byte) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if strings.Contains(payload, `"result"`) || strings.Contains(payload, `"error"`) {
			return []byte(payload), nil
		}
	}
	return nil, errors.New("MCP 的 SSE 响应里没有可用报文")
}

// clip 截断日志用文本。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 本文件是「带工具」的流式调用（第 6 课调用循环的底座）。
//
// 与 CompleteStream 的差别只有两点：
//  1. 请求体多了 tools / tool_choice；
//  2. 解析时除 delta.content 外还要**按 index 累积 delta.tool_calls**。
//
// 第 2 点是实测踩出来的：真实网关把一次工具调用拆成十几片，第一片给
// id/type/name，其后每片只给 {"index":0,"function":{"arguments":"片段"}}，
// arguments 是**被切开的字符串**，必须拼完再交给 json.Unmarshal。
//
// 思维链（delta.reasoning_content）不累积、也不下发：它不是给用户看的正文。

// StreamResult 是一次流式调用的结果。
type StreamResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
}

// CompleteStreamTools 带工具清单做流式调用，边收正文边回调 onDelta。
//
// 若模型这一轮只要求调工具、没有正文，onDelta 不会被调用，ToolCalls 非空；
// 调用方执行工具后把结果回填进 messages 再来一轮。
func (c *ChatClient) CompleteStreamTools(
	ctx context.Context,
	messages []ChatMessage,
	tools []ToolSpec,
	onDelta func(string) error,
) (StreamResult, error) {
	var out StreamResult

	req0 := chatRequest{Model: c.model, Messages: messages, Stream: true}
	if len(tools) > 0 {
		req0.Tools = tools
		req0.ToolChoice = "auto"
	}
	body, err := json.Marshal(req0)
	if err != nil {
		return out, fmt.Errorf("序列化流式请求: %w", err)
	}
	c.dump(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("构造流式请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	client := &http.Client{Transport: c.http.Transport}
	res, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("对话网关不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return out, fmt.Errorf("对话网关返回 HTTP %d: %s", res.StatusCode, truncateForLog(string(raw), 200))
	}

	var full strings.Builder
	// 按 index 累积工具调用分片
	type acc struct {
		id, name string
		args     strings.Builder
	}
	accs := map[int]*acc{}
	maxIdx := -1

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			out.FinishReason = ch.FinishReason
		}
		for _, tc := range ch.Delta.ToolCalls {
			a := accs[tc.Index]
			if a == nil {
				a = &acc{}
				accs[tc.Index] = a
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			a.args.WriteString(tc.Function.Arguments)
			if tc.Index > maxIdx {
				maxIdx = tc.Index
			}
		}
		if d := ch.Delta.Content; d != "" {
			full.WriteString(d)
			if onDelta != nil {
				if err := onDelta(d); err != nil {
					return out, err
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("读取流式响应失败: %w", err)
	}
	out.Content = full.String()
	for i := 0; i <= maxIdx; i++ {
		a := accs[i]
		if a == nil || a.name == "" {
			continue
		}
		args := a.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:   a.id,
			Type: "function",
			Function: ToolCallFunction{
				Name:      a.name,
				Arguments: args,
			},
		})
	}
	return out, nil
}

// dump 在设置了观测钩子时把请求体原样交出去（取证用，默认 nil 不做事）。
func (c *ChatClient) dump(body []byte) {
	if c.Dump != nil {
		c.Dump(body)
	}
}

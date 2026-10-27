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

// 本文件是对话网关的**流式**客户端（第 5 课 SSE 链路）。
//
// 与 Complete 的区别：请求带 "stream": true，网关按 OpenAI SSE 协议逐行返回
// `data: {json}`，其中 choices[0].delta.content 是增量片段；`data: [DONE]` 结束。
//
// 我们**不**把这些原始分片直接透传给浏览器，而是由 conversation 层转成自有事件
// （meta / delta / done）—— 对外契约是课程自己的，不是网关的。

// streamChunk 是网关返回的一个 SSE 分片。
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// CompleteStream 以流式方式调对话网关，每收到一段增量就调用 onDelta。
//
// 返回拼好的全文；任何错误（网络/非 2xx/解析）都归一为 error。
// onDelta 若返回 error 会中止读取（用于"客户端断连就停"）。
func (c *ChatClient) CompleteStream(ctx context.Context, messages []ChatMessage, onDelta func(string) error) (string, error) {
	body, err := json.Marshal(chatRequest{Model: c.model, Messages: messages, Stream: true})
	if err != nil {
		return "", fmt.Errorf("序列化流式请求: %w", err)
	}
	c.dump(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造流式请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	// 流式响应可能较长，超时放宽；用 context 控制总时长而非 client.Timeout。
	client := &http.Client{Transport: c.http.Transport}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("对话网关不可达: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return "", fmt.Errorf("对话网关返回 HTTP %d: %s", res.StatusCode, truncateForLog(string(raw), 200))
	}

	var full strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // 单行可能较大
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 心跳/注释行
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 容忍无法解析的分片
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		full.WriteString(delta)
		if onDelta != nil {
			if err := onDelta(delta); err != nil {
				return full.String(), err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return full.String(), fmt.Errorf("读取流式响应失败: %w", err)
	}
	return full.String(), nil
}

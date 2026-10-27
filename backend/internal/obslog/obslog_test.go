package obslog

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// capture 把日志输出换成一个 buffer，测完还原。
//
// std.out 是本包私有字段，测试同包可直接读写，不必为测试加导出接口。
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	std.mu.Lock()
	old := std.out
	std.mu.Unlock()

	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(old) })
	return &buf
}

func TestLinesAreSingleLineJSON(t *testing.T) {
	buf := capture(t)
	Info("db.bootstrap_done", "建表完成", "count", 4)

	line := strings.TrimSuffix(buf.String(), "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("一条日志出现换行: %q", line)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("不是合法 JSON: %v / %q", err, line)
	}
	if rec["level"] != LevelInfo || rec["event"] != "db.bootstrap_done" {
		t.Fatalf("固定键不正确: %v", rec)
	}
	if rec["count"] != float64(4) {
		t.Fatalf("自定义字段丢失: %v", rec)
	}
}

// msg 为空时整个键不出现；没有 request_id 时不写 "-"。
func TestEmptyMsgAndMissingRequestIDOmitted(t *testing.T) {
	buf := capture(t)
	Error("mcp.connect_failed", "")

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec["msg"]; ok {
		t.Fatalf("空 msg 不应该出现该键: %v", rec)
	}
	if _, ok := rec["request_id"]; ok {
		t.Fatalf("没有 request_id 时不应该写该键: %v", rec)
	}
}

// 一次请求内的日志必须带上同一个 request_id，且固定键不允许被覆盖。
func TestContextCarriesRequestIDAndFixedKeysNotOverridable(t *testing.T) {
	buf := capture(t)
	ctx := WithRequestID(context.Background(), "demo-001")
	ErrorCtx(ctx, "material.upload_failed", "入库失败", "level", "debug", "detail", "boom")

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["request_id"] != "demo-001" {
		t.Fatalf("request_id 丢失: %v", rec)
	}
	if rec["level"] != LevelError {
		t.Fatalf("level 被调用方覆盖了: %v", rec)
	}
	if rec["detail"] != "boom" {
		t.Fatalf("业务字段丢失: %v", rec)
	}
}

// 奇数个 kv 不 panic，退化成 odd_kv 标记。
func TestOddKVDoesNotPanic(t *testing.T) {
	buf := capture(t)
	Info("http.access", "", "status", 200, "dangling")

	if !strings.Contains(buf.String(), `"odd_kv":true`) {
		t.Fatalf("奇数 kv 未标记: %q", buf.String())
	}
}

func TestNewRequestIDIsHex16(t *testing.T) {
	id := NewRequestID()
	if len(id) != 16 {
		t.Fatalf("长度应为 16，实际 %d: %q", len(id), id)
	}
	if NewRequestID() == id {
		t.Fatalf("两次生成的标识相同: %q", id)
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("出现非十六进制字符: %q", id)
		}
	}
}

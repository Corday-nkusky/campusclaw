package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 回归守卫：statusRecorder 必须透传 http.Flusher。
//
// 背景（2026-10-14 实测踩坑）：logRequests 用 statusRecorder 包住 ResponseWriter
// 以记录状态码，但嵌入 http.ResponseWriter **接口**只会带来 Header/Write/WriteHeader，
// 包装后 http.Flusher 丢失。第 5 课 SSE 端点靠 w.(http.Flusher) 逐段冲刷，断言一旦失败
// 就直接 500（错误体 {"error":"stream_unsupported"}）——整个流式回答不可用。
//
// 本测试用 httptest.ResponseRecorder（它实现 Flusher）包一层，断言包装后仍是 Flusher，
// 且 Flush() 真的透传到底层。
func TestStatusRecorder_ImplementsFlusher(t *testing.T) {
	under := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: under, status: http.StatusOK}

	f, ok := interface{}(rec).(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder 未实现 http.Flusher —— SSE 端点会 500（stream_unsupported）")
	}
	f.Flush()
	if !under.Flushed {
		t.Fatal("statusRecorder.Flush() 未透传到底层 writer")
	}
}

// statusRecorder 仍必须是 http.ResponseWriter（WriteHeader 记录状态码）。
func TestStatusRecorder_RecordsStatus(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	rec.WriteHeader(http.StatusCreated)
	if rec.status != http.StatusCreated {
		t.Fatalf("status=%d，期望 201", rec.status)
	}
}

// Unwrap 必须返回底层 writer，供 http.ResponseController 穿透。
func TestStatusRecorder_Unwrap(t *testing.T) {
	under := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: under}
	if rec.Unwrap() != http.ResponseWriter(under) {
		t.Fatal("Unwrap 未返回底层 writer")
	}
}

package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

// 网关报 success 但输出 0 token：流正常结束、content 与 tool_calls 都为空。应重试一次，仍为空就明确报错。
func TestEmptyStreamRetriesOnceThenFails(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{ID: "inner-x", DisplayName: "inner-x", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err == nil || !IsEmpty(err) || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("err=%v hits=%d, want empty error after 2 attempts", err, hits)
	}
}

// 非流式 JSON 空回复同样按空返回处理，不能把空消息交给 Cursor。
func TestEmptyStaticJSONIsEmptyError(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if !IsEmpty(err) {
		t.Fatalf("err=%v, want empty error", err)
	}
}

// 发给网关的模型名是配置里的上游模型名，不带 Cursor 侧显示用的 inner- 前缀。
func TestUpstreamModelNameIsConfiguredName(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"}}]}`)+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{ID: "inner-DeepSeek-V4", DisplayName: "inner-DeepSeek-V4", Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "DeepSeek-V4"}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if got != "DeepSeek-V4" {
		t.Fatalf("model=%q", got)
	}
}

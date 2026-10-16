package provider

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func captureLog(t *testing.T) *lockedBuf {
	buf := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// 空回复要记下 finish_reason、用量和上游原始响应前缀；密钥和请求体不能出现在日志里。
func TestEmptyReplyLogsDiagnosticsWithoutSecrets(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })
	logs := captureLog(t)
	const key = "sk-secret-key-123456"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":""},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":321,"completion_tokens":0},"echo":"`+key+`"}`)+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: key, Model: "m"}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "request-body-marker"}}, nil, func(string) error { return nil }, nil)
	if !IsEmpty(err) {
		t.Fatalf("err=%v", err)
	}
	out := logs.String()
	for _, want := range []string{"upstream_empty_detail", "finish_reason=content_filter", "prompt_tokens=321", "raw_head="} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, key) || strings.Contains(out, "request-body-marker") {
		t.Fatalf("log leaks secret or request body: %s", out)
	}
}

func TestHeadBufferKeepsOnlyPrefix(t *testing.T) {
	h := &headBuffer{max: 4}
	_, _ = h.Write([]byte("abc"))
	n, _ := h.Write([]byte("defg"))
	if string(h.Bytes()) != "abcd" || n != 4 {
		t.Fatalf("%q %d", h.Bytes(), n)
	}
}

// 带很多图片的请求空回复时不原样重发，交给调用方省略旧图片。
func TestEmptyWithManyImagesIsNotResent(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })
	captureLog(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	msgs := []Message{{Role: "user", Content: "hi"}}
	for i := 0; i < ImageFloor+1; i++ {
		msgs = append(msgs, toolImage(10))
	}
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", msgs, nil, func(string) error { return nil }, nil)
	if !EmptyLarge(err) || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
	if got := Explain(MarkCompacted(err)); got != "上游空回复，可能上下文过大，已尝试压缩" {
		t.Fatalf("explain=%q", got)
	}
}

package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

func imageHistory(n int) []provider.Message {
	msgs := []provider.Message{{Role: "user", Content: "read pages"}}
	for i := 0; i < n; i++ {
		msgs = append(msgs, provider.Message{Role: "tool", ToolCallID: "c", Content: "Read image file: p.png", Images: []provider.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}})
	}
	return msgs
}

// 带很多图片的请求空回复：省略一批最旧的图片后再试一次，第二次请求里的图片应变少。
func TestEmptyWithImagesRetriesOnceWithFewerImages(t *testing.T) {
	var hits int32
	var counts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		counts = append(counts, strings.Count(string(body), "image_url"))
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&hits, 1) == 1 {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msgs, reply, err := chatOrCompact(context.Background(), m, dialer.Direct(), "", imageHistory(12), nil, func(string) error { return nil }, nil)
	if err != nil || reply.Content != "ok" {
		t.Fatalf("err=%v reply=%q", err, reply.Content)
	}
	if len(counts) != 2 || counts[1] >= counts[0] {
		t.Fatalf("image counts per request: %v", counts)
	}
	if !strings.Contains(msgs[1].Content, provider.ImageOmitted) {
		t.Fatalf("oldest image not replaced: %q", msgs[1].Content)
	}
}

// 重试后仍空回复：给用户的说明要提到已尝试压缩。
func TestEmptyAfterImageRetryExplainsCompaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, _, err := chatOrCompact(context.Background(), m, dialer.Direct(), "", imageHistory(12), nil, func(string) error { return nil }, nil)
	if got := provider.Explain(err); !strings.Contains(got, "上游空回复，可能上下文过大，已尝试压缩") {
		t.Fatalf("explain=%q", got)
	}
}

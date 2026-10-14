package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

func keepaliveOnly(hits *int32, stop <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for {
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			f.Flush()
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

// 中转只发 keepalive 注释行、迟迟不发 data 帧时，要按数据空闲超时结束，且只重试一次，尽快向 Cursor 报错。
func TestKeepaliveOnlyStreamTimesOutAndRetriesOnce(t *testing.T) {
	streamDataIdle, retryBase = 60*time.Millisecond, time.Millisecond
	t.Cleanup(func() { streamDataIdle, retryBase = 150*time.Second, 500*time.Millisecond })
	var hits int32
	stop := make(chan struct{})
	srv := httptest.NewServer(keepaliveOnly(&hits, stop))
	t.Cleanup(func() { close(stop); srv.Close() })
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	done := make(chan error, 1)
	go func() {
		_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !IsDataIdle(err) {
			t.Fatalf("err=%v, want data idle timeout", err)
		}
		if n := atomic.LoadInt32(&hits); n != 2 {
			t.Fatalf("hits=%d, want 2 (one retry)", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive-only stream hung")
	}
}

// 推理模型首字节慢：keepalive 一段时间后再来 data，只要没超过阈值就正常完成。
func TestSlowFirstDataWithinLimitSucceeds(t *testing.T) {
	streamDataIdle = 300 * time.Millisecond
	t.Cleanup(func() { streamDataIdle = 150 * time.Second })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, ": keepalive\n\n")
			f.Flush()
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"}}]}`)+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil || msg.Content != "ok" {
		t.Fatalf("msg=%q err=%v", msg.Content, err)
	}
}

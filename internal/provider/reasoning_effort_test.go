package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

// 端点用闭集拒绝 Cursor 传来的 reasoning_effort 档位时，去掉字段重试一次；第二轮不带该字段。
func TestChatRetriesWithoutRejectedReasoningEffort(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if strings.Contains(string(body), `"reasoning_effort"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"'reasoning_effort' must be one of: 'low', 'high'","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"}}]}`, "[DONE]"))
	}))
	defer srv.Close()
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", Reasoning: true, Effort: "medium"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "ok" {
		t.Fatalf("content %q", msg.Content)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"reasoning_effort":"medium"`) {
		t.Fatalf("requests: %d %v", len(bodies), bodies)
	}
	if strings.Contains(bodies[1], "reasoning_effort") {
		t.Fatalf("retry still sent the rejected field: %s", bodies[1])
	}
	// 同一端点+模型的下一次调用直接不发这个字段，不再先撞一次 400。
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "again"}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 3 || strings.Contains(bodies[2], "reasoning_effort") {
		t.Fatalf("remembered route still sent the field: %d %v", len(bodies), bodies)
	}
}

// 别的参数报枚举错误时不能顺手去掉 reasoning_effort 重试。
func TestChatKeepsOtherBadRequestsTerminal(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"'tool_choice' must be one of none, auto, required","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", Reasoning: true, Effort: "medium"}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err == nil {
		t.Fatal("expected the 400 to surface")
	}
	if requests != 1 {
		t.Fatalf("other 400 was retried %d times", requests)
	}
}

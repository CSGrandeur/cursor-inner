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

// 事件顺序取自一个 OpenAI 兼容服务商的 Responses 流：先推理、再正文，用量在 response.completed 里。
// 以前测速按 Chat 的 choices 解析，一个字也读不到，结果是 0 token、0.0 tok/s。
func TestSpeedTestReadsResponsesStream(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"count"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m1","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"m1","content_index":0,"delta":"1 2 3"}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"m1","content_index":0,"delta":" 4 5"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"1 2 3 4 5"}]}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":73,"output_tokens":62,"output_tokens_details":{"reasoning_tokens":21}}}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			_, _ = io.WriteString(w, "data: "+e+"\n\n")
		}
	}))
	defer srv.Close()
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	got := Test(context.Background(), m, dialer.Direct(), nil)
	if !got.OK || got.Output != "1 2 3 4 5" || got.OutputTokens != 62 || got.TokensPerSecond <= 0 || got.FirstValidResponseMS == nil {
		t.Fatalf("%+v", got)
	}
}

func TestSpeedTestReadsResponsesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"1 2 3"}]}],"usage":{"output_tokens":9}}`)
	}))
	defer srv.Close()
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	got := Test(context.Background(), m, dialer.Direct(), nil)
	if !got.OK || got.Output != "1 2 3" || got.OutputTokens != 9 {
		t.Fatalf("%+v", got)
	}
}

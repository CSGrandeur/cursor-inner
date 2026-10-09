package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/runlog"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func serve(t *testing.T, body string, seen *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatOpenAIStreamsTextAndToolCalls(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"choices":[{"delta":{"content":"Let me read "}}]}`,
		`{"choices":[{"delta":{"content":"it.","tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"/a.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"Grep","arguments":"{}"}}]}}]}`,
		"[DONE]",
	), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	var streamed strings.Builder
	msg, err := Chat(context.Background(), m, dialer.Direct(), "sys", []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Name: "Glob", Arguments: `{"glob_pattern":"*"}`}}},
		{Role: "tool", ToolCallID: "c0", Content: "a.go"},
	}, []Tool{{Name: "Read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}, func(s string) error {
		streamed.WriteString(s)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Let me read it." || msg.Content != "Let me read it." {
		t.Fatalf("text %q / %q", streamed.String(), msg.Content)
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0] != (ToolCall{ID: "call_1", Name: "Read", Arguments: `{"path":"/a.go"}`}) || msg.ToolCalls[1].Name != "Grep" {
		t.Fatalf("%+v", msg.ToolCalls)
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(seen, &req); err != nil {
		t.Fatal(err)
	}
	if req.Messages[0]["role"] != "system" || req.Messages[2]["content"] != nil || req.Messages[3]["tool_call_id"] != "c0" || req.Tools[0]["type"] != "function" {
		t.Fatalf("%s", seen)
	}
	if strings.Contains(string(seen), "max_tokens") {
		t.Fatalf("openai request should not cap max_tokens: %s", seen)
	}
}

func TestChatAnthropicStreamsToolUseAndMergesResults(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reading."}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"/b.go\"}"}}`,
		`{"type":"message_stop"}`,
	), &seen)
	m := config.Model{Type: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "sys", []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "Glob", Arguments: "{}"}, {ID: "b", Name: "Glob", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "a", Content: "x"},
		{Role: "tool", ToolCallID: "b", Content: "y", IsError: true},
	}, []Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}}, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Reading." || len(msg.ToolCalls) != 1 || msg.ToolCalls[0] != (ToolCall{ID: "toolu_1", Name: "Read", Arguments: `{"path":"/b.go"}`}) {
		t.Fatalf("%+v", msg)
	}
	var req struct {
		System []struct {
			Text  string         `json:"text"`
			Cache map[string]any `json:"cache_control"`
		} `json:"system"`
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(seen, &req); err != nil {
		t.Fatal(err)
	}
	last := req.Messages[2].Content[1]
	if len(req.System) != 1 || req.System[0].Text != "sys" || req.System[0].Cache["type"] != "ephemeral" || len(req.Messages) != 3 || len(req.Messages[2].Content) != 2 || last["is_error"] != true || last["cache_control"] == nil || req.Tools[0]["input_schema"] == nil {
		t.Fatalf("%s", seen)
	}
}

func TestReasoningEchoAndStrip(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "a", Reasoning: ""},
		{Role: "tool", ToolCallID: "t", Content: "ok"},
		{Role: "assistant", Reasoning: "chain", ToolCalls: []ToolCall{{ID: "t", Name: "Read", Arguments: "{}"}}},
	}
	var seen []byte
	srv := serve(t, sse(
		`{"choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"more"}}]}`,
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
	), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "deepseek-v4-pro", DisplayName: "inner"}
	var thought strings.Builder
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", history[:1], nil, func(string) error { return nil }, func(s string) error {
		thought.WriteString(s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if thought.String() != "think more" || msg.Reasoning != "think more" {
		t.Fatalf("thought %q reasoning %q", thought.String(), msg.Reasoning)
	}

	srv = serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m.BaseURL = srv.URL
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", history, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	var echoed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(seen, &echoed); err != nil {
		t.Fatal(err)
	}
	if echoed.Messages[1]["reasoning_content"] != " " || echoed.Messages[3]["reasoning_content"] != "chain" {
		t.Fatalf("echo %s", seen)
	}

	srv = serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	strict := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "mistral-small"}
	if _, err := Chat(context.Background(), strict, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "a", Reasoning: "secret"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seen), "reasoning_content") || strings.Contains(string(seen), "secret") {
		t.Fatalf("strict endpoint kept reasoning: %s", seen)
	}
}

func TestAnthropicReplaysSignedThinking(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`), &seen)
	m := config.Model{Type: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "before", Reasoning: "old", ReasoningSignature: "old-sig", ToolCalls: []ToolCall{{ID: "a", Name: "Read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "a", Content: "x"},
	}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Reasoning != "hmm" || msg.ReasoningSignature != "sig" || msg.Content != "ok" {
		t.Fatalf("%+v", msg)
	}
	var req struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(seen, &req); err != nil {
		t.Fatal(err)
	}
	first := req.Messages[1].Content[0]
	if first["type"] != "thinking" || first["thinking"] != "old" || first["signature"] != "old-sig" {
		t.Fatalf("%v", req.Messages[1].Content)
	}
}

func TestTruncatedToolCallIsDropped(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"ok","function":{"name":"Read","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"cut","function":{"name":"Write","arguments":"{\"path\":"}}]},"finish_reason":"length"}]}`,
	), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Truncated || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "ok" {
		t.Fatalf("%+v", msg)
	}
}

func TestToolResultCarriesTheImage(t *testing.T) {
	png := []byte{1, 2, 3}
	tool := Message{Role: "tool", ToolCallID: "c1", Content: "Read image file: /w/a.png", Images: []Image{{MIME: "image/png", Data: png}}}
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "look"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Arguments: "{}"}}}, tool}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"role":"tool"`) || !strings.Contains(string(seen), "image_url") || !strings.Contains(string(seen), "data:image/png;base64,AQID") {
		t.Fatalf("%s", seen)
	}
	seen = nil
	srv = serve(t, sse(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"message_stop"}`,
	), &seen)
	m.Type = "anthropic"
	m.BaseURL = srv.URL
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "look"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Arguments: "{}"}}}, tool}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"type":"tool_result"`) || !strings.Contains(string(seen), `"media_type":"image/png"`) || !strings.Contains(string(seen), "AQID") {
		t.Fatalf("%s", seen)
	}
}

func TestReasoningEffortFastAndImage(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", Effort: "high", Fast: true}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{
		Role: "user", Content: "see", Images: []Image{{MIME: "image/png", Data: []byte{1, 2, 3}}},
	}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"reasoning_effort":"high"`) || !strings.Contains(string(seen), `"service_tier":"fast"`) || !strings.Contains(string(seen), "image_url") {
		t.Fatalf("%s", seen)
	}
}

func TestUnsetEffortOmitsReasoningField(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seen), "reasoning_effort") {
		t.Fatalf("%s", seen)
	}
	if anthropicEffort("xhigh") != "high" || anthropicEffort("max") != "high" || anthropicEffort("low") != "low" {
		t.Fatal(anthropicEffort("xhigh"))
	}
}

func TestUsagePromptTokensAreKept(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":42}}`,
	), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil || msg.PromptTokens != 42 {
		t.Fatalf("%v %+v", err, msg)
	}
}

func TestMaxOutputTokensIsSentWhenConfigured(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m", MaxOutputTokens: 123}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"max_tokens":123`) {
		t.Fatalf("%s", seen)
	}
}

func TestRetryRateLimitThenSuccess(t *testing.T) {
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "slow down")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil || msg.Content != "ok" || hits != 2 {
		t.Fatalf("%v %q hits=%d", err, msg.Content, hits)
	}
}

func TestNoRetryAfterTextIsEmitted(t *testing.T) {
	streamIdle = 30 * time.Millisecond
	retryBase = time.Millisecond
	t.Cleanup(func() {
		streamIdle = 120 * time.Second
		retryBase = 500 * time.Millisecond
	})
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse(`{"choices":[{"delta":{"content":"partial"}}]}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(200 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err == nil || !Retryable(err) || hits != 1 {
		t.Fatalf("%v hits=%d", err, hits)
	}
}

func TestCloseDanglingFillsMissingResults(t *testing.T) {
	out := CloseDangling([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "Read"}, {ID: "b", Name: "Grep"}}},
		{Role: "tool", ToolCallID: "a", Content: "file"},
	})
	if len(out) != 4 || out[2].ToolCallID != "b" || !out[2].IsError || out[2].Content != interruptedTool || out[3].Content != interruptedTurn {
		t.Fatalf("%+v", out)
	}
}

func TestChatNoteRecordsBytesNotText(t *testing.T) {
	secret := "super-secret-reply"
	var notes []runlog.Note
	runlog.Set(func(n runlog.Note) { notes = append(notes, n) })
	t.Cleanup(func() { runlog.Set(nil) })
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"`+secret+`"}}]}`, "[DONE]"), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
	_, err := Chat(runlog.WithRequest(context.Background(), "req-1"), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sawBytes, sawRequest bool
	for _, n := range notes {
		if n.Kind == "model_text" && n.Bytes == len(secret) && n.Request == "req-1" {
			sawBytes = true
		}
		if n.Kind == "model_request" && n.Bytes > 0 {
			sawRequest = true
		}
		if strings.Contains(n.Error, secret) || strings.Contains(n.Detail, secret) || strings.Contains(n.Model, secret) {
			t.Fatalf("note kept the reply: %+v", n)
		}
	}
	if !sawBytes || !sawRequest {
		t.Fatalf("notes %+v", notes)
	}
}

func TestExplainNamesTheRetry(t *testing.T) {
	err := withRetries(&APIError{Kind: KindRateLimit, Status: 429, Text: "endpoint returned 429"}, 2)
	if got := Explain(err); got != "429 限流，重试 2 次后失败" {
		t.Fatal(got)
	}
	if got := Explain(context.Canceled); got != "已停止" {
		t.Fatal(got)
	}
}

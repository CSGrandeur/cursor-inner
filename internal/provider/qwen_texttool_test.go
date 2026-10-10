package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

func TestEnableThinkingChatTemplateKwargs(t *testing.T) {
	off := qwenBody(t, config.Model{Model: "qwen3-32b", Type: "openai-chat"}, false)
	if off["enable_thinking"] != false {
		t.Errorf("off enable_thinking=%v", off["enable_thinking"])
	}
	ctk, _ := off["chat_template_kwargs"].(map[string]any)
	if ctk == nil || ctk["enable_thinking"] != false {
		t.Errorf("off chat_template_kwargs=%v", off["chat_template_kwargs"])
	}
	on := qwenBody(t, config.Model{Model: "qwen3-32b", Type: "openai-chat", Reasoning: true, Effort: "low"}, false)
	if on["enable_thinking"] != true {
		t.Errorf("on enable_thinking=%v", on["enable_thinking"])
	}
	if _, ok := on["chat_template_kwargs"]; ok {
		t.Error("reasoning on must not force chat_template_kwargs")
	}
}

func TestTextToolSystemRescueRoundTrip(t *testing.T) {
	tools := []Tool{{Name: "StrReplace", Description: "edit", Parameters: json.RawMessage(`{"type":"object"}`)}}
	sys := textToolSystem("base", tools)
	if !strings.Contains(sys, "<tool_call>") || !strings.Contains(sys, "StrReplace") {
		t.Fatalf("system prompt missing instructions: %q", sys)
	}
	content := "I'll edit it.\n<tool_call>\n{\"name\": \"StrReplace\", \"arguments\": {\"path\": \"/a\", \"old_string\": \"1\", \"new_string\": \"2\"}}\n</tool_call>"
	msg := applyRescue(Message{Role: "assistant", Content: content}, tools)
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "StrReplace" {
		t.Fatalf("rescue failed: %+v", msg.ToolCalls)
	}
	var args map[string]any
	if json.Unmarshal([]byte(msg.ToolCalls[0].Arguments), &args) != nil || args["new_string"] != "2" {
		t.Fatalf("args not parsed: %q", msg.ToolCalls[0].Arguments)
	}
}

func TestWantsTextToolModeAndDetail(t *testing.T) {
	vllm := statusError(400, []byte(`{"error":{"message":"\"auto\" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set","type":"BadRequestError"}}`), "")
	if !wantsTextToolMode(vllm) {
		t.Error("vLLM tool-parser 400 should trigger text tool mode")
	}
	if wantsTextToolMode(statusError(400, []byte(`{"error":{"message":"invalid value for temperature"}}`), "")) {
		t.Error("unrelated 400 should not trigger")
	}
	if got := Explain(vllm); !strings.Contains(got, "tool-call-parser") || !strings.Contains(got, "请求被拒绝") {
		t.Errorf("Explain should surface upstream detail: %q", got)
	}
}

func TestUpstreamDetailRedactsAndCaps(t *testing.T) {
	d := upstreamDetail([]byte(`{"error":{"message":"bad key sk-ABCDEF0123456789 rejected"}}`))
	if strings.Contains(d, "sk-ABCDEF0123456789") || !strings.Contains(d, "[redacted]") {
		t.Errorf("secret not redacted: %q", d)
	}
	long := `{"message":"` + strings.Repeat("x", 500) + `"}`
	if r := []rune(upstreamDetail([]byte(long))); len(r) > 201 {
		t.Errorf("detail not capped: %d runes", len(r))
	}
}

func TestChatFlipsToTextToolMode(t *testing.T) {
	var calls int32
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"\"auto\" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(
			`{"choices":[{"delta":{"content":"<tool_call>\n{\"name\": \"Read\", \"arguments\": {\"path\": \"/a\"}}\n</tool_call>"}}]}`,
			"[DONE]",
		))
	}))
	t.Cleanup(srv.Close)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "qwen3vl-32b-fliptest"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "sys",
		[]Message{{Role: "user", Content: "edit"}},
		[]Tool{{Name: "Read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "Read" {
		t.Fatalf("want rescued Read call, got %+v", msg.ToolCalls)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("want 2 requests, got %d", calls)
	}
	if len(bodies) >= 2 && strings.Contains(string(bodies[1]), `"tools"`) {
		t.Error("second request must not send tools")
	}
	if !TextToolActive(m) {
		t.Error("endpoint should be remembered as text-tool mode")
	}
}

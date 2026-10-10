package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestQwenVariantLimits(t *testing.T) {
	cases := []struct {
		model string
		ctx   int
		out   int
	}{
		{"qwen3-coder-plus", 1000000, 65536},
		{"qwen3-coder-flash", 1000000, 65536},
		{"qwen3-coder-480b-a35b", 262144, 65536},
		{"qwen3-max", 262144, 32768},
		{"qwen3.5", 1000000, 65536},
		{"qwen-plus-latest", 1000000, 32768},
		{"qwq-32b", 131072, 32768},
		{"qwen2.5-coder:32b", 262144, 32768},
		{"openai/qwen2.5-coder:7b", 262144, 32768},
		{"deepseek-chat", 0, 0},
	}
	for _, c := range cases {
		gc, go_ := qwenVariantLimits(c.model)
		if gc != c.ctx || go_ != c.out {
			t.Errorf("%s: got (%d,%d) want (%d,%d)", c.model, gc, go_, c.ctx, c.out)
		}
	}
}

func TestQwenThinkingBudget(t *testing.T) {
	mk := func(model, effort string, reasoning bool) config.Model {
		return config.Model{Model: model, Reasoning: reasoning, Effort: effort}
	}
	if qwenThinkingBudget(mk("qwen3-coder-plus", "", true)) != nil {
		t.Error("no effort -> nil budget")
	}
	if b := qwenThinkingBudget(mk("qwen3-coder-plus", "high", true)); b == nil || *b != 24576 {
		t.Errorf("high -> 24576, got %v", b)
	}
	if qwenThinkingBudget(mk("qwen3-coder-plus", "low", false)) != nil {
		t.Error("reasoning off -> nil budget")
	}
	if qwenThinkingBudget(mk("qwq-32b", "low", true)) != nil {
		t.Error("qwq is not hybrid-thinking -> nil budget")
	}
	if qwenThinkingBudget(mk("deepseek-chat", "low", true)) != nil {
		t.Error("non-qwen -> nil budget")
	}
}

func qwenBody(t *testing.T, m config.Model, withTools bool) map[string]any {
	t.Helper()
	req := chatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}
	if withTools {
		req.Tools = []Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}
	}
	raw, err := openAIBody(m, req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenAIBodyQwenFields(t *testing.T) {
	m := config.Model{Model: "qwen3-coder-plus", Type: "openai-chat", Reasoning: true, Effort: "low"}
	b := qwenBody(t, m, true)
	if b["enable_thinking"] != true {
		t.Errorf("enable_thinking=%v", b["enable_thinking"])
	}
	if b["thinking_budget"] != float64(1024) {
		t.Errorf("thinking_budget=%v", b["thinking_budget"])
	}
	if b["parallel_tool_calls"] != true {
		t.Errorf("parallel_tool_calls=%v", b["parallel_tool_calls"])
	}
	so, _ := b["stream_options"].(map[string]any)
	if so == nil || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage=%v", b["stream_options"])
	}

	// reasoning off: enable_thinking present as false, no budget.
	off := qwenBody(t, config.Model{Model: "qwen3-coder-plus", Type: "openai-chat"}, true)
	if off["enable_thinking"] != false {
		t.Errorf("off enable_thinking=%v", off["enable_thinking"])
	}
	if _, ok := off["thinking_budget"]; ok {
		t.Error("off must not send thinking_budget")
	}

	// no tools: no parallel_tool_calls.
	noTools := qwenBody(t, m, false)
	if _, ok := noTools["parallel_tool_calls"]; ok {
		t.Error("parallel_tool_calls must be omitted without tools")
	}

	// non-qwen: no qwen-only fields.
	ds := qwenBody(t, config.Model{Model: "deepseek-chat", Type: "openai-chat"}, true)
	if _, ok := ds["enable_thinking"]; ok {
		t.Error("non-qwen must not send enable_thinking")
	}
	if _, ok := ds["parallel_tool_calls"]; ok {
		t.Error("non-qwen must not send parallel_tool_calls")
	}
}

func TestOpenAIContentQwenVLImage(t *testing.T) {
	parts, ok := openAIContent(Message{Role: "user", Content: "what is this", Images: []Image{{MIME: "image/png", Data: []byte("abc")}}}).([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("want text+image parts, got %T %v", parts, parts)
	}
	img, _ := parts[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("want image_url, got %v", img["type"])
	}
	url, _ := img["image_url"].(map[string]string)
	if url == nil || !strings.HasPrefix(url["url"], "data:image/png;base64,") {
		t.Fatalf("bad data URI: %v", img["image_url"])
	}
}

func feed(t *testing.T, a *accumulator, line string) {
	t.Helper()
	if _, err := a.feed([]byte(line)); err != nil {
		t.Fatal(err)
	}
}

func TestAccumulatorOllamaNoIndexMultiCall(t *testing.T) {
	a := newAccumulator("openai-chat", false)
	feed(t, a, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"Read","arguments":"{\"path\":\"/a\"}"}}]}}]}`)
	feed(t, a, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"Shell","arguments":"{\"command\":\"ls\"}"}}]}}]}`)
	msg := a.message()
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("want 2 calls, got %d: %+v", len(msg.ToolCalls), msg.ToolCalls)
	}
	if msg.ToolCalls[0].Name != "Read" || msg.ToolCalls[0].Arguments != `{"path":"/a"}` {
		t.Errorf("call0 = %+v", msg.ToolCalls[0])
	}
	if msg.ToolCalls[1].Name != "Shell" || msg.ToolCalls[1].Arguments != `{"command":"ls"}` {
		t.Errorf("call1 = %+v", msg.ToolCalls[1])
	}
}

func TestAccumulatorNormalSplitArgs(t *testing.T) {
	a := newAccumulator("openai-chat", false)
	feed(t, a, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"Read","arguments":"{\"path\":"}}]}}]}`)
	feed(t, a, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/a\"}"}}]}}]}`)
	msg := a.message()
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Arguments != `{"path":"/a"}` {
		t.Fatalf("split args not reassembled: %+v", msg.ToolCalls)
	}
}

func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) {
		t.Fatalf("not an APIError: %v", err)
	}
	return api.Kind
}

func TestDashscopeErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		code      int
		body      string
		want      Kind
		retryable bool
		overflow  bool
	}{
		{"throttling-on-400", 400, `{"code":"Throttling.RateQuota","message":"Requests rate limit exceeded"}`, KindRateLimit, true, false},
		{"data-inspection", 400, `{"error":{"code":"DataInspectionFailed","message":"content filter triggered"}}`, KindBadRequest, false, false},
		{"arrearage", 400, `{"code":"Arrearage","message":"insufficient balance"}`, KindAuth, false, false},
		{"input-overflow", 400, `{"code":"InvalidParameter","message":"Range of input length should be [1, 129024]"}`, KindContextOverflow, false, true},
		{"internal-500", 500, `{"code":"InternalError","message":"internal error"}`, KindServer, true, false},
		{"plain-429", 429, `{"message":"too many requests"}`, KindRateLimit, true, false},
	}
	for _, c := range cases {
		err := statusError(c.code, []byte(c.body), "")
		if k := kindOf(t, err); k != c.want {
			t.Errorf("%s: kind=%d want %d", c.name, k, c.want)
		}
		if Retryable(err) != c.retryable {
			t.Errorf("%s: retryable=%v want %v", c.name, Retryable(err), c.retryable)
		}
		if Overflow(err) != c.overflow {
			t.Errorf("%s: overflow=%v want %v", c.name, Overflow(err), c.overflow)
		}
	}
}

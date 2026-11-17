package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cursor-inner/internal/config"
)

func TestAnthropicBodyToollessDegradesToolHistoryToText(t *testing.T) {
	m := config.Model{Type: "anthropic", Model: "claude-sonnet-4-5"}
	msgs := []Message{
		{Role: "user", Content: "check file"},
		{Role: "assistant", Content: "reading", ToolCalls: []ToolCall{{ID: "t1", Name: "Read", Arguments: `{"path":"a.go"}`}}},
		{Role: "tool", ToolCallID: "t1", Content: "package a"},
		{Role: "user", Content: "summarize"},
	}

	rawNoTools, err := anthropicBody(m, chatRequest{System: "sys", Messages: msgs, MaxTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	sNoTools := string(rawNoTools)
	if strings.Contains(sNoTools, `"tool_use"`) || strings.Contains(sNoTools, `"tool_result"`) {
		t.Fatalf("toolless anthropic body must not contain tool_use or tool_result blocks: %s", sNoTools)
	}
	if !strings.Contains(sNoTools, "[Tool call Read(t1):") || !strings.Contains(sNoTools, "[Tool result (t1): package a]") {
		t.Fatalf("expected textualized tool history, got: %s", sNoTools)
	}

	rawWithTools, err := anthropicBody(m, chatRequest{
		System:    "sys",
		Messages:  msgs,
		Tools:     []Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawWithTools), `"tool_use"`) || !strings.Contains(string(rawWithTools), `"tool_result"`) {
		t.Fatalf("anthropic body with tools must keep native tool blocks: %s", rawWithTools)
	}
}

func TestResponsesBodyToolBatchOrderingAndStaticMessage(t *testing.T) {
	m := config.Model{Type: "openai-responses", Model: "gpt-5"}
	png := []byte{1, 2, 3}
	msgs := []Message{
		{Role: "user", Content: "see", Images: []Image{{MIME: "image/png", Data: png}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Arguments: `{}`}, {ID: "c2", Name: "Read", Arguments: `{}`}}},
		{Role: "tool", ToolCallID: "c1", Content: "img1", Images: []Image{{MIME: "image/png", Data: png}}},
		{Role: "tool", ToolCallID: "c2", Content: "txt2"},
	}
	raw, err := responsesBody(m, chatRequest{Messages: msgs, MaxTokens: 256})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// user, fc1, fc2, fco1, fco2, user(attached image)
	if len(doc.Input) != 6 {
		t.Fatalf("expected 6 input items, got %d: %s", len(doc.Input), raw)
	}
	if doc.Input[0].Content[1].Type != "input_image" || !strings.HasPrefix(doc.Input[0].Content[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("user image missing: %+v", doc.Input[0])
	}
	if doc.Input[3].Type != "function_call_output" || doc.Input[4].Type != "function_call_output" || doc.Input[5].Role != "user" {
		t.Fatalf("tool outputs must stay contiguous before attached image message: %s", raw)
	}
	if doc.Input[5].Content[1].Type != "input_image" {
		t.Fatalf("tool image missing: %+v", doc.Input[5])
	}

	staticRaw := []byte(`{"status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"enc1","summary":[{"text":"think"}]},{"type":"function_call","call_id":"call_9","name":"Read","arguments":"{\"path\":\"x\"}"},{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":4}}}`)
	msg, err := staticMessage(staticRaw, "openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "done" || msg.Reasoning != "think" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_9" || msg.CacheTokens != 4 {
		t.Fatalf("unexpected static responses message: %+v", msg)
	}
}

func TestGeminiThoughtSignatureRoundTripAndForeignStrip(t *testing.T) {
	acc := newAccumulator("openai-chat", false)
	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","extra_content":{"google":{"thought_signature":"sig-abc"}},"function":{"name":"Read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`
	if _, err := acc.feed([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	reply := acc.message()
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ThoughtSignature != "sig-abc" {
		t.Fatalf("thought_signature lost on stream: %+v", reply.ToolCalls)
	}

	gemini := config.Model{Type: "openai-chat", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-2.5-pro"}
	reply.ReasoningBy = ReasoningIssuer(gemini)
	raw, err := openAIBody(gemini, chatRequest{Messages: []Message{{Role: "user", Content: "hi"}, reply}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"thought_signature":"sig-abc"`) {
		t.Fatalf("openAIBody did not emit thought_signature: %s", raw)
	}

	other := config.Model{Type: "openai-chat", BaseURL: "https://api.openai.com/v1", Model: "gpt-4.1"}
	stripped := StripForeignReasoning(other, []Message{reply})
	if stripped[0].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("foreign thought_signature must be stripped when switching model: %+v", stripped[0].ToolCalls)
	}
}

func TestChatRetryDropsRejectedReasoningSignatureOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		if n == 1 {
			if !strings.Contains(string(raw), `"signature":"stale-sig"`) {
				t.Errorf("first attempt should carry original signature: %s", raw)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid signature in thinking block"}}`))
			return
		}
		if strings.Contains(string(raw), `"signature"`) {
			t.Errorf("retry must strip invalid signature: %s", raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"recovered\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
	}))
	defer srv.Close()

	m := config.Model{Type: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4-5"}
	msgs := []Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1", Reasoning: "old thought", ReasoningSignature: "stale-sig"},
		{Role: "user", Content: "q2"},
	}
	got, err := Chat(context.Background(), m, nil, "", msgs, nil, nil, nil)
	if err != nil {
		t.Fatalf("expected signature-strip recovery, got err: %v", err)
	}
	if got.Content != "recovered" || calls.Load() != 2 {
		t.Fatalf("got %q calls=%d", got.Content, calls.Load())
	}
}

func TestEnsureUniqueToolCallIDsFillsEmptyAndDeduplicates(t *testing.T) {
	history := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "rescue-1", Name: "Read"}, {ID: "call_1", Name: "Glob"}}},
		{Role: "tool", ToolCallID: "rescue-1", Content: "ok"},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	}
	got := ensureUniqueToolCallIDs(history, []ToolCall{
		{ID: "", Name: "Read"},
		{ID: "rescue-1", Name: "Grep"},
		{ID: "rescue-1", Name: "Glob"},
	})
	if got[0].ID != "call_2" || got[1].ID != "rescue-1_2" || got[2].ID != "rescue-1_3" {
		t.Fatalf("unexpected deduplicated IDs: %+v", got)
	}
}

func TestDetectFamilyModelNameBeatsDashScopeHostAndGLMLimits(t *testing.T) {
	kimiOnBailian := config.Model{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "kimi-k3"}
	if f := DetectFamily(kimiOnBailian); f != FamilyKimi {
		t.Fatalf("kimi on dashscope got %v, want FamilyKimi", f)
	}
	glmOnBailian := config.Model{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "glm-5.2"}
	if f := DetectFamily(glmOnBailian); f != FamilyGLM {
		t.Fatalf("glm on dashscope got %v, want FamilyGLM", f)
	}
	if ctxW, outW := glmVariantLimits("zhipu/glm-5.2:latest"); ctxW != 1000000 || outW != 131072 {
		t.Fatalf("glm-5.2 limits = %d/%d, want 1000000/131072", ctxW, outW)
	}
	if ctxW, outW := glmVariantLimits("glm-5"); ctxW != 202752 || outW != 131072 {
		t.Fatalf("glm-5 limits = %d/%d, want 202752/131072", ctxW, outW)
	}
}

func TestEmptyReplyContentFilterAndRefusalClassifiedAsInspection(t *testing.T) {
	m := config.Model{Model: "test-model"}
	for _, finish := range []string{"content_filter", "refusal"} {
		err := emptyReply(m, nil, 10, finish, Message{}, nil)
		if !ContentInspection(err) || Retryable(err) || IsEmpty(err) {
			t.Fatalf("finish=%s: inspection=%v retryable=%v empty=%v", finish, ContentInspection(err), Retryable(err), IsEmpty(err))
		}
	}
	dsErr := statusError(400, []byte(`{"error":{"message":"Content Exists Risk","type":"invalid_request_error"}}`), nil)
	if !ContentInspection(dsErr) || Retryable(dsErr) {
		t.Fatalf("DeepSeek Content Exists Risk must be classified as non-retryable inspection: %v", dsErr)
	}
}

func TestModelsDevReadsNestedLimitAndDiscoverStatusFirst(t *testing.T) {
	var raw any
	_ = json.Unmarshal([]byte(`{"providers":{"x":{"models":{"demo-model":{"id":"demo-model","reasoning":true,"tool_call":true,"limit":{"context":200000,"output":64000}}}}}}`), &raw)
	out := map[string]modelsDevEntry{}
	walkModelsDev(raw, out)
	e, ok := out["demo-model"]
	if !ok || e.ContextWindow != 200000 || e.MaxOutputTokens != 64000 || !e.Reasoning || !e.Tools {
		t.Fatalf("walkModelsDev nested limit failed: %+v", e)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<html>401 Unauthorized</html>"))
	}))
	defer srv.Close()
	_, err := DiscoverModels(context.Background(), DiscoverInput{Type: "openai-chat", BaseURL: srv.URL, APIKey: "bad"}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401 status in error, got: %v", err)
	}
}

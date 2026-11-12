package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

func TestEncodeDecodeReasoningReplay(t *testing.T) {
	got := decodeReasoningReplay(encodeReasoningReplay([]reasoningReplayItem{
		{ID: "rs_1", Enc: "enc-a", Summary: "think a"},
		{ID: "  ", Enc: "skip-no-id"},
		{ID: "rs_2", Enc: "", Summary: "skip-no-enc"},
		{ID: "rs_3", Enc: "enc-c"},
	}))
	if len(got) != 2 || got[0].ID != "rs_1" || got[0].Enc != "enc-a" || got[0].Summary != "think a" || got[1].ID != "rs_3" || got[1].Enc != "enc-c" {
		t.Fatalf("got %+v", got)
	}
	if encodeReasoningReplay(nil) != "" || encodeReasoningReplay([]reasoningReplayItem{{Enc: "bare"}}) != "" {
		t.Fatal("unreplayable items must not be encoded")
	}
}

func TestDecodeReasoningReplayRejectsLegacyAndForeign(t *testing.T) {
	for _, sig := range []string{
		"",
		"enc_blob_from_older_inner",
		"old-sig",
		`{"id":"rs_1","encrypted_content":"enc"}`,
		`{"items":[{"id":"rs_1","encrypted_content":"enc"}]}`,
		`[{"id":"rs_1","encrypted_content":"enc"}]`,
		`{"rs_items":[{"id":"","encrypted_content":"enc"},{"id":"rs_1","encrypted_content":""}]}`,
	} {
		if got := decodeReasoningReplay(sig); got != nil {
			t.Fatalf("legacy or foreign %q -> %+v", sig, got)
		}
	}
}

func responsesInput(t *testing.T, messages []Message) []map[string]any {
	t.Helper()
	raw, err := responsesBody(config.Model{Type: "openai-responses", Model: "qwen4-max-preview"}, chatRequest{
		Messages:  messages,
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rs_prev") {
		t.Fatalf("invented id rs_prev still present: %s", raw)
	}
	return body.Input
}

func reasoningInputs(input []map[string]any) []map[string]any {
	var out []map[string]any
	for _, item := range input {
		if item["type"] == "reasoning" {
			out = append(out, item)
		}
	}
	return out
}

func TestResponsesReplayUsesOfficialIDsAndKeepsMultiple(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","delta":"first think"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"enc-1"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"Read","arguments":""}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"Read","arguments":"{\"path\":\"/a\"}"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"reasoning","id":"rs_2"}}`,
		`{"type":"response.reasoning_text.delta","output_index":2,"item_id":"rs_2","delta":" second"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","id":"rs_2","encrypted_content":"enc-2"}}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_b","name":"Shell","arguments":""}}`,
		`{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_b","name":"Shell","arguments":"{\"command\":\"uname\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	if msg.Truncated {
		t.Fatalf("completed stream must not be truncated: %+v", msg)
	}
	if msg.Reasoning != "first think second" {
		t.Fatalf("reasoning=%q", msg.Reasoning)
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0].ID != "call_a" || msg.ToolCalls[1].ID != "call_b" {
		t.Fatalf("calls=%+v", msg.ToolCalls)
	}
	packed := decodeReasoningReplay(msg.ReasoningSignature)
	if len(packed) != 2 || packed[0].ID != "rs_1" || packed[0].Enc != "enc-1" || packed[0].Summary != "first think" || packed[1].ID != "rs_2" || packed[1].Enc != "enc-2" || packed[1].Summary != " second" {
		t.Fatalf("packed=%+v sig=%s", packed, msg.ReasoningSignature)
	}

	input := responsesInput(t, []Message{
		{Role: "user", Content: "hi"},
		msg,
		{Role: "tool", ToolCallID: "call_a", Content: "a"},
		{Role: "tool", ToolCallID: "call_b", Content: "Linux"},
	})
	rs := reasoningInputs(input)
	if len(rs) != 2 || rs[0]["id"] != "rs_1" || rs[0]["encrypted_content"] != "enc-1" || rs[1]["id"] != "rs_2" || rs[1]["encrypted_content"] != "enc-2" {
		t.Fatalf("replay items=%v input=%v", rs, input)
	}
	// function_call / function_call_output pairing and order stay on the existing path.
	var types []string
	for _, item := range input {
		types = append(types, item["type"].(string))
	}
	wantPrefix := []string{"message", "reasoning", "reasoning", "function_call", "function_call", "function_call_output", "function_call_output"}
	if len(types) < len(wantPrefix) {
		t.Fatalf("input types=%v", types)
	}
	for i, w := range wantPrefix {
		if types[i] != w {
			t.Fatalf("input types=%v", types)
		}
	}
}

func TestResponsesLegacySignatureIsNotReplayedWithInventedID(t *testing.T) {
	input := responsesInput(t, []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "done", Reasoning: "old think", ReasoningSignature: "legacy-encrypted-blob", ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Arguments: `{"path":"/a"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: "a"},
	})
	if rs := reasoningInputs(input); len(rs) != 0 {
		t.Fatalf("legacy signature was replayed: %v", rs)
	}
	var sawCall, sawOut, sawContent bool
	for _, item := range input {
		switch item["type"] {
		case "function_call":
			sawCall = item["call_id"] == "c1"
		case "function_call_output":
			sawOut = item["call_id"] == "c1"
		case "message":
			if item["role"] == "assistant" && item["content"] != nil {
				sawContent = true
			}
		}
	}
	if !sawCall || !sawOut || !sawContent {
		t.Fatalf("tool pairing or assistant content dropped: %v", input)
	}
}

func TestResponsesReasoningWithoutEncryptedContentLeavesSignatureEmpty(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"visible think"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	if msg.Reasoning != "visible think" || msg.ReasoningSignature != "" {
		t.Fatalf("unreplayable thinking should stay visible but not packed: %+v", msg)
	}
}

func TestResponsesIncompleteMarksTruncationAndDropsOnlyTail(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_text.delta","item_id":"m1","delta":"working"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"ok","name":"Read"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_1","arguments":"{\"path\":\"/a\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"ok","name":"Read","arguments":"{\"path\":\"/a\"}"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"cut","name":"Write"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_2","delta":"{\"path\":"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":11,"output_tokens":22}}}`,
	)
	if !msg.Truncated || msg.Content != "working" || msg.PromptTokens != 11 || msg.CompletionTokens != 22 {
		t.Fatalf("%+v", msg)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "ok" || msg.ToolCalls[0].Arguments != `{"path":"/a"}` {
		t.Fatalf("complete tool must be kept, incomplete tail dropped: %+v", msg.ToolCalls)
	}
}

func TestResponsesIncompleteWithoutCompletedStillCountsUsage(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_text.delta","delta":"half"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":3,"output_tokens":4}}}`,
	)
	if !msg.Truncated || msg.Content != "half" || len(msg.ToolCalls) != 0 || msg.PromptTokens != 3 || msg.CompletionTokens != 4 {
		t.Fatalf("%+v", msg)
	}
}

func TestResponsesIncompleteContentFilterIsNotLengthTruncation(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_text.delta","delta":"ok"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":1,"output_tokens":2}}}`,
	)
	if msg.Truncated || msg.Content != "ok" || msg.PromptTokens != 1 {
		t.Fatalf("content_filter must not take the length-continue path: %+v", msg)
	}
}

func TestResponsesCompletedAfterIncompleteKeepsTruncationReason(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_text.delta","delta":"half"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":5,"output_tokens":6}}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":6}}}`,
	)
	if !msg.Truncated || msg.Content != "half" {
		t.Fatalf("completed must not clear an earlier max_output_tokens incomplete: %+v", msg)
	}
}

func TestResponsesChatIncompleteFollowsExistingTruncationPath(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"ok","name":"Read"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"ok","name":"Read","arguments":"{}"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"cut","name":"Write"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_2","delta":"{\"path\":"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":9,"output_tokens":8}}}`,
	), &seen)
	m := config.Model{Type: "openai-responses", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Truncated || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "ok" || msg.CompletionTokens != 8 {
		t.Fatalf("%+v", msg)
	}
}

func TestAnthropicSignatureIsUnchangedByResponsesReplayEncoding(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	), &seen)
	m := config.Model{Type: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	msg, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ReasoningSignature != "sig" || msg.Reasoning != "hmm" || msg.Content != "ok" {
		t.Fatalf("%+v", msg)
	}
	if decodeReasoningReplay(msg.ReasoningSignature) != nil {
		t.Fatalf("anthropic signature must not parse as a Responses envelope: %q", msg.ReasoningSignature)
	}
}

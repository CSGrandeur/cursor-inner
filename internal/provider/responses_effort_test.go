package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

func responsesTextOK() string {
	return sse(
		`{"type":"response.output_text.delta","delta":"OK"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
}

func sentReasoningEffort(body []byte) (string, bool) {
	var parsed struct {
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}
	if parsed.Reasoning == nil {
		return "", false
	}
	return parsed.Reasoning.Effort, true
}

func chatCapture(t *testing.T, m config.Model, ctx context.Context, seen *[]byte) {
	t.Helper()
	if _, err := Chat(ctx, m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if len(*seen) == 0 {
		t.Fatal("Chat() did not send a request body")
	}
}

func TestResponsesChatPassesCursorEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			var seen []byte
			srv := serve(t, responsesTextOK(), &seen)
			m := config.Model{
				Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
				Model: "qwen4-max-preview", Reasoning: true, Effort: effort,
			}
			chatCapture(t, m, context.Background(), &seen)
			got, ok := sentReasoningEffort(seen)
			if !ok || got != effort {
				t.Fatalf("cursor %s -> reasoning.effort=%q present=%v body=%s", effort, got, ok, seen)
			}
		})
	}
}

func TestResponsesChatDefaultOmitsEffort(t *testing.T) {
	var seen []byte
	srv := serve(t, responsesTextOK(), &seen)
	m := config.Model{
		Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
		Model: "qwen4-max-preview", Reasoning: true,
	}
	chatCapture(t, m, context.Background(), &seen)
	if got, ok := sentReasoningEffort(seen); ok {
		t.Fatalf("empty selection must omit reasoning.effort, got %q body=%s", got, seen)
	}
}

func TestResponsesChatProbeEffortOverridesUser(t *testing.T) {
	var seen []byte
	srv := serve(t, responsesTextOK(), &seen)
	m := config.Model{
		Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
		Model: "qwen4-max-preview", Reasoning: true, Effort: "xhigh",
	}
	ctx := withProbeOpts(context.Background(), probeOpts{ReasoningEffort: "low"})
	chatCapture(t, m, ctx, &seen)
	got, ok := sentReasoningEffort(seen)
	if !ok || got != "low" {
		t.Fatalf("probe must override user xhigh with low, got %q present=%v body=%s", got, ok, seen)
	}
}

func TestResponsesChatStaleHighMaxUseDefault(t *testing.T) {
	for _, stale := range []string{"high", "max"} {
		t.Run(stale, func(t *testing.T) {
			var seen []byte
			srv := serve(t, responsesTextOK(), &seen)
			m := config.Model{
				Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
				Model: "qwen4-max-preview", Reasoning: true, Effort: stale,
			}
			chatCapture(t, m, context.Background(), &seen)
			got, ok := sentReasoningEffort(seen)
			if !ok || got != "xhigh" {
				t.Fatalf("stale %s must not be sent, want default xhigh, got %q present=%v body=%s", stale, got, ok, seen)
			}
			if strings.Contains(string(seen), `"effort":"high"`) || strings.Contains(string(seen), `"effort":"max"`) {
				t.Fatalf("unsupported value leaked: %s", seen)
			}
		})
	}
}

func TestResponsesChatOtherProviderKeepsGenericEffort(t *testing.T) {
	for _, effort := range []string{"high", "max", "medium"} {
		t.Run(effort, func(t *testing.T) {
			var seen []byte
			srv := serve(t, responsesTextOK(), &seen)
			m := config.Model{
				Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
				Model: "some-other-model", Reasoning: true, Effort: effort,
			}
			chatCapture(t, m, context.Background(), &seen)
			got, ok := sentReasoningEffort(seen)
			if !ok || got != effort {
				t.Fatalf("generic model must pass %s, got %q present=%v body=%s", effort, got, ok, seen)
			}
		})
	}
}

func TestChatCompletionsQwen4SanitizesStaleHigh(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{
		Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k",
		Model: "qwen4-max-preview", Reasoning: true, Effort: "high",
	}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"reasoning_effort":"xhigh"`) {
		t.Fatalf("qwen4 chat completions stale high should become xhigh: %s", seen)
	}
	if strings.Contains(string(seen), `"reasoning_effort":"high"`) {
		t.Fatalf("unsupported high leaked: %s", seen)
	}
}

func TestChatCompletionsOtherProviderUnchanged(t *testing.T) {
	var seen []byte
	srv := serve(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &seen)
	m := config.Model{Type: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "deepseek-chat", Effort: "high"}
	if _, err := Chat(context.Background(), m, dialer.Direct(), "", []Message{{Role: "user", Content: "hi"}}, nil, func(string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), `"reasoning_effort":"high"`) {
		t.Fatalf("other provider high must pass through: %s", seen)
	}
}

func TestResponsesCustomEffortLevels(t *testing.T) {
	var seen []byte
	srv := serve(t, responsesTextOK(), &seen)
	m := config.Model{
		Type: "openai-responses", BaseURL: srv.URL + "/v1", APIKey: "k",
		Model: "qwen4-max-preview", Reasoning: true, Effort: "medium",
		EffortLevels: []string{"low", "xhigh"}, DefaultEffort: "xhigh",
	}
	chatCapture(t, m, context.Background(), &seen)
	got, ok := sentReasoningEffort(seen)
	if !ok || got != "xhigh" {
		t.Fatalf("medium outside custom list must fall back to default xhigh, got %q present=%v body=%s", got, ok, seen)
	}
}

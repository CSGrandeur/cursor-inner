package provider

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestRequestURL(t *testing.T) {
	cases := []struct {
		in, want string
		kind     string
	}{
		{"https://api.example.com", "https://api.example.com/v1/chat/completions", "openai-chat"},
		{"https://api.example.com/v1", "https://api.example.com/v1/chat/completions", "openai-chat"},
		{"https://api.example.com/v1/chat/completions", "https://api.example.com/v1/chat/completions", "openai-chat"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/messages", "anthropic"},
	}
	for _, tc := range cases {
		got, err := RequestURL(config.Model{Type: tc.kind, BaseURL: tc.in})
		if err != nil || got != tc.want {
			t.Fatalf("%s -> %s (%v), want %s", tc.in, got, err, tc.want)
		}
	}
}

func TestDeltaLine(t *testing.T) {
	got, ok := deltaLine(`data: {"choices":[{"delta":{"content":"你"}}]}`, "openai-chat")
	if !ok || got != "你" {
		t.Fatal(got, ok)
	}
	got, ok = deltaLine(`data: {"delta":{"text":"好"}}`, "anthropic")
	if !ok || got != "好" {
		t.Fatal(got, ok)
	}
	if _, ok := deltaLine("data: [DONE]", "openai-chat"); ok {
		t.Fatal("done")
	}
}

func TestPrepareStableID(t *testing.T) {
	a, err := Prepare(config.Model{DisplayName: "甲", Type: "openai-chat", BaseURL: "https://x", APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(config.Model{DisplayName: "甲", Type: "openai-chat", BaseURL: "https://x", APIKey: "k", Model: "m"})
	if err != nil || a.ID != b.ID || strings.TrimSpace(a.ID) == "" {
		t.Fatal(a.ID, b.ID, err)
	}
}

func TestEstimateOutputTokens(t *testing.T) {
	if estimateOutputTokens("1 2 3 4") != 4 {
		t.Fatal(estimateOutputTokens("1 2 3 4"))
	}
	if estimateOutputTokens("") != 0 {
		t.Fatal("empty")
	}
	if estimateOutputTokens("abcd") != 1 {
		t.Fatal(estimateOutputTokens("abcd"))
	}
}

func TestParseSSEUsage(t *testing.T) {
	event := parseSSE(`data: {"choices":[{"delta":{"content":"1"}}],"usage":{"completion_tokens":12}}`, "openai-chat")
	if !event.textOK || event.text != "1" || !event.usageOK || event.usage != 12 {
		t.Fatalf("%+v", event)
	}
	event = parseSSE(`data: {"type":"message_delta","usage":{"output_tokens":9}}`, "anthropic")
	if event.textOK || !event.usageOK || event.usage != 9 {
		t.Fatalf("%+v", event)
	}
}

package provider

import (
	"testing"

	"cursor-inner/internal/config"
)

func TestDetectFamily(t *testing.T) {
	cases := []struct {
		m    config.Model
		want Family
	}{
		{config.Model{Model: "deepseek-chat", BaseURL: "https://api.deepseek.com"}, FamilyDeepSeek},
		{config.Model{Model: "qwen-plus", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode"}, FamilyQwen},
		{config.Model{Model: "moonshot-v1", BaseURL: "https://api.moonshot.cn"}, FamilyKimi},
		{config.Model{DisplayName: "Kimi", Model: "kimi-k2", BaseURL: "https://example.com"}, FamilyKimi},
		{config.Model{Model: "glm-4", BaseURL: "https://open.bigmodel.cn/api/paas"}, FamilyGLM},
		{config.Model{Model: "gemini-2.0-flash", BaseURL: "https://generativelanguage.googleapis.com"}, FamilyGemini},
		{config.Model{Model: "gpt-4.1", BaseURL: "https://api.openai.com"}, FamilyGPT},
		{config.Model{Type: "anthropic", Model: "claude-sonnet", BaseURL: "https://api.anthropic.com"}, FamilyClaude},
		{config.Model{Model: "mimo-v2", BaseURL: "https://api.xiaomimimo.com"}, FamilyMiMo},
		{config.Model{Model: "custom-x", BaseURL: "https://llm.example.com"}, FamilyOther},
	}
	for _, tc := range cases {
		if got := DetectFamily(tc.m); got != tc.want {
			t.Fatalf("%+v -> %s want %s", tc.m, got, tc.want)
		}
	}
}

func TestNeedsReasoningEchoUsesFamily(t *testing.T) {
	if !needsReasoningEcho(config.Model{Model: "deepseek-v4", BaseURL: "https://api.deepseek.com"}) {
		t.Fatal("deepseek")
	}
	if !needsReasoningEcho(config.Model{Model: "kimi-k2", BaseURL: "https://api.moonshot.cn"}) {
		t.Fatal("kimi")
	}
	if needsReasoningEcho(config.Model{Model: "gpt-4.1", BaseURL: "https://api.openai.com"}) {
		t.Fatal("gpt must not echo")
	}
}

func TestQwenEnableThinking(t *testing.T) {
	cases := []struct {
		model     string
		reasoning bool
		want      *bool
	}{
		{"qwen3-coder-plus", true, boolp(true)},
		{"qwen3-coder-plus", false, boolp(false)},
		{"qwen2.5-coder-32b", true, nil},
		{"qwq-32b", true, nil},
		{"deepseek-chat", true, nil},
	}
	for _, c := range cases {
		got := qwenEnableThinking(config.Model{Model: c.model, Reasoning: c.reasoning})
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("%s reasoning=%v: got %v want %v", c.model, c.reasoning, got, c.want)
		}
	}
}

func boolp(b bool) *bool { return &b }

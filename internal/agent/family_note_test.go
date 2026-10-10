package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestFamilyNote(t *testing.T) {
	if familyNote(config.Model{Model: "claude-sonnet-4-5", Type: "anthropic"}) != "" {
		t.Fatal("claude needs no extra notes")
	}
	for _, name := range []string{"deepseek-chat", "qwen3-coder-plus", "kimi-k2", "glm-4.6", "some-local-model"} {
		note := familyNote(config.Model{Model: name})
		if !strings.Contains(note, "function-calling interface") || !strings.HasPrefix(note, "\n<model_notes>") {
			t.Fatalf("%s: %q", name, note)
		}
	}
	if !strings.Contains(familyNote(config.Model{Model: "gpt-5"}), "apply_patch") {
		t.Fatal("gpt note should mention apply_patch")
	}
}

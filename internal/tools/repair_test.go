package tools

import (
	"strings"
	"testing"
)

func TestRepairArguments(t *testing.T) {
	got, ok := RepairArguments("```json\n{\"path\":\"/a.go\",}\n```")
	if !ok || got != `{"path":"/a.go"}` {
		t.Fatalf("%q %v", got, ok)
	}
	got, ok = RepairArguments(`{"path":"/a.go"`)
	if !ok || got != `{"path":"/a.go"}` {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := RepairArguments(`{"a":`); ok {
		t.Fatal("unrepairable object accepted")
	}
}

func TestClipForModelKeepsEnds(t *testing.T) {
	text := strings.Repeat("a", 20*1024) + strings.Repeat("b", 20*1024)
	got := ClipForModel(text)
	if len(got) >= len(text) || !strings.HasPrefix(got, strings.Repeat("a", 100)) || !strings.HasSuffix(got, strings.Repeat("b", 100)) || !strings.Contains(got, "truncated") {
		t.Fatalf("len %d", len(got))
	}
	if ClipForModel("short") != "short" {
		t.Fatal("short text changed")
	}
}

package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/provider"
)

func TestEstimateUsesReportedPromptTokens(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: strings.Repeat("x", 4000)},
		{Role: "assistant", Content: "short", PromptTokens: 1000},
		{Role: "user", Content: strings.Repeat("y", 400)},
	}
	if got := estimateTokens(messages); got < 1000 || got > 1200 {
		t.Fatalf("%d", got)
	}
}

func TestCompactLeavesShortHistoryAlone(t *testing.T) {
	in := []provider.Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "short"}}
	out := compactMessages(in, 128000)
	if out[1].Content != "short" {
		t.Fatal(out[1].Content)
	}
}

func TestRecentTailKeepsToolResultsWithTheirCall(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "read both"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: "tool", ToolCallID: "a"},
		{Role: "tool", ToolCallID: "b"},
	}
	tail := recentTail(messages, 2)
	if len(tail) != 3 || tail[0].Role != "assistant" {
		t.Fatalf("%+v", tail)
	}
}

func TestCompactShrinksOldToolOutput(t *testing.T) {
	old := strings.Repeat("x", 2000)
	messages := []provider.Message{{Role: "user", Content: "start"}}
	for i := 0; i < 8; i++ {
		messages = append(messages, provider.Message{Role: "tool", Content: old})
	}
	out := compactMessages(messages, 1000)
	if !strings.Contains(out[1].Content, "compacted") {
		t.Fatal("old tool output was kept in full")
	}
	if strings.Contains(out[len(out)-1].Content, "compacted") {
		t.Fatal("the newest tool output was compacted")
	}
}

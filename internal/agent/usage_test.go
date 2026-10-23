package agent

import (
	"encoding/json"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func TestTokenBreakdownCategoriesMatchCursorUI(t *testing.T) {
	system := "base system\n<rules>\n<rule path=\"a\">be brief</rule>\n</rules>\n" +
		"<agent_skills>\n<skill path=\"s\">do X</skill>\n</agent_skills>\n" +
		"<subagents>\nexplore\n</subagents>\n" +
		"<mcp_meta_tools>\nmeta\n</mcp_meta_tools>\n"
	tools := []provider.Tool{
		{Name: "Read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_click", Description: "click", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	mcp := map[string]struct{}{"browser_click": {}}
	messages := []provider.Message{
		{Role: "user", Content: "<summary>\nearlier work\n</summary>"},
		{Role: "user", Content: "hello there"},
		{Role: "assistant", Content: "hi", Reasoning: "think"},
	}
	snap := tokenBreakdown(1000, 128000, nil, system, tools, mcp, messages)
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.GetTotalUsedTokens() != 1000 || snap.GetMaxTokens() != 128000 {
		t.Fatalf("totals %+v", snap)
	}
	got := map[string]uint32{}
	for _, c := range snap.GetCategories() {
		got[c.GetId()] = c.GetEstimatedTokens()
		if c.GetLabel() == "" {
			t.Fatalf("empty label for %s", c.GetId())
		}
	}
	wantIDs := []string{
		"system_prompt", "tools", "rules", "skills", "mcp", "subagents",
		"summarized_conversation", "conversation",
	}
	for _, id := range wantIDs {
		if _, ok := got[id]; !ok {
			t.Fatalf("missing category %s in %#v", id, got)
		}
	}
	if got["tools"] == 0 || got["mcp"] == 0 {
		t.Fatalf("tools/mcp should be nonzero: %#v", got)
	}
	if got["rules"] == 0 || got["skills"] == 0 || got["subagents"] == 0 {
		t.Fatalf("tagged system sections should be nonzero: %#v", got)
	}
	if got["summarized_conversation"] == 0 {
		t.Fatalf("summary should be nonzero: %#v", got)
	}
	var sum uint32
	for _, id := range wantIDs {
		sum += got[id]
	}
	if sum != 1000 {
		t.Fatalf("categories sum %d want 1000: %#v", sum, got)
	}
}

func TestTokenBreakdownFitsWhenSpecialExceedsUsed(t *testing.T) {
	system := "x"
	tools := []provider.Tool{{Name: "Read", Description: string(make([]byte, 8000)), Parameters: json.RawMessage(`{}`)}}
	snap := tokenBreakdown(10, 100, nil, system, tools, nil, nil)
	var sum uint32
	for _, c := range snap.GetCategories() {
		sum += c.GetEstimatedTokens()
	}
	if sum != 10 {
		t.Fatalf("sum %d", sum)
	}
}

func TestTokenBreakdownReusesSummaryFromBaseline(t *testing.T) {
	chars := uint32(100)
	baseline := &cursorpb.PromptTokenBreakdownSnapshot{
		Categories: []*cursorpb.PromptTokenBreakdownCategory{{
			Id: "summarized_conversation", Label: "Summarized conversation", EstimatedTokens: 42, CharacterCount: &chars,
		}},
	}
	snap := tokenBreakdown(100, 1000, baseline, "sys", nil, nil, []provider.Message{
		{Role: "user", Content: "only chat"},
	})
	var summary uint32
	for _, c := range snap.GetCategories() {
		if c.GetId() == "summarized_conversation" {
			summary = c.GetEstimatedTokens()
		}
	}
	if summary == 0 {
		t.Fatal("expected baseline summary to contribute")
	}
}

func TestWriteCheckpointIncludesBreakdown(t *testing.T) {
	tools := []provider.Tool{{Name: "Shell", Description: "run", Parameters: json.RawMessage(`{}`)}}
	built, err := writeCheckpoint("system text", "req", []provider.Message{
		{Role: "user", Content: "hi"},
	}, 0, 128000, 500, "", 0, nil, nil, nil, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	bd := built.State.GetTokenDetails().GetBreakdown()
	if bd == nil || len(bd.GetCategories()) < 8 {
		t.Fatalf("breakdown %+v", bd)
	}
	if built.State.GetTokenDetails().GetPromptContextUsageTree() != nil {
		t.Fatal("tree should stay unset")
	}
	if len(built.State.GetTokenDetails().GetPromptContextUsageSnapshotBlobId()) != 0 {
		t.Fatal("blob id should stay unset")
	}
}

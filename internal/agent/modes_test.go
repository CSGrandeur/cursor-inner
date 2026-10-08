package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
	"cursor-inner/internal/tools"
)

func TestSemSearchIsInEverySearchMode(t *testing.T) {
	all := tools.Catalog()
	modes := []cursorpb.AgentMode{
		cursorpb.AgentMode_AGENT_MODE_AGENT,
		cursorpb.AgentMode_AGENT_MODE_ASK,
		cursorpb.AgentMode_AGENT_MODE_PLAN,
		cursorpb.AgentMode_AGENT_MODE_DEBUG,
		cursorpb.AgentMode_AGENT_MODE_MULTITASK,
	}
	for _, mode := range modes {
		if !hasTool(toolCatalog(mode, all), "SemSearch") {
			t.Fatalf("mode %s has no SemSearch", mode)
		}
	}
	if !hasTool(subagentCatalog("explore", all), "SemSearch") {
		t.Fatal("explore has no SemSearch")
	}
	if !hasTool(subagentCatalog("general", all), "SemSearch") {
		t.Fatal("subagent has no SemSearch")
	}
}

func TestDebugModeFillsTheLogEndpoint(t *testing.T) {
	note := modeNote(cursorpb.AgentMode_AGENT_MODE_DEBUG, &cursorpb.RequestContext{
		DebugModeConfig: &cursorpb.DebugModeConfig{
			ServerEndpoint: "http://127.0.0.1:9/log",
			LogPath:        "/tmp/debug.ndjson",
			SessionId:      "sess-1",
		},
	})
	for _, want := range []string{"http://127.0.0.1:9/log", "/tmp/debug.ndjson", "sess-1"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(note, "{{DEBUG_") {
		t.Fatal("debug placeholder left in the prompt")
	}
}

func hasTool(list []provider.Tool, name string) bool {
	for _, tool := range list {
		if tool.Name == name {
			return true
		}
	}
	return false
}

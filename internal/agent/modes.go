package agent

import (
	_ "embed"
	"fmt"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

//go:embed modes/ask.md
var askMode string

//go:embed modes/plan.md
var planMode string

//go:embed modes/debug.md
var debugMode string

//go:embed modes/multitask.md
var multitaskMode string

//go:embed modes/subagent.md
var subagentMode string

func toolCatalog(mode cursorpb.AgentMode, all []provider.Tool) []provider.Tool {
	allowed, ok := modeTools[mode]
	if !ok {
		return all
	}
	var out []provider.Tool
	for _, tool := range all {
		if allowed[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}

var modeTools = map[cursorpb.AgentMode]map[string]bool{
	cursorpb.AgentMode_AGENT_MODE_AGENT:     toolSet("Shell", "Grep", "Delete", "WebSearch", "WebFetch", "GenerateImage", "EditNotebook", "TodoWrite", "StrReplace", "Write", "Read", "ReadLints", "Glob", "AskQuestion", "Task", "SemSearch", "GetMcpTools", "FetchMcpResource", "SwitchMode", "CallMcpTool"),
	cursorpb.AgentMode_AGENT_MODE_ASK:       toolSet("AskQuestion", "CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "ReadLints", "Shell", "StrReplace", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write", "SemSearch"),
	cursorpb.AgentMode_AGENT_MODE_PLAN:      toolSet("Shell", "Glob", "Grep", "Read", "TodoWrite", "ReadLints", "WebSearch", "WebFetch", "AskQuestion", "CreatePlan", "Task", "FetchMcpResource", "CallMcpTool", "SemSearch"),
	cursorpb.AgentMode_AGENT_MODE_DEBUG:     toolSet("AskQuestion", "CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "ReadLints", "Shell", "StrReplace", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write", "SemSearch"),
	cursorpb.AgentMode_AGENT_MODE_MULTITASK: toolSet("AskQuestion", "CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "ReadLints", "Shell", "StrReplace", "SwitchMode", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write", "GenerateImage", "SemSearch"),
}

var exploreTools = toolSet("Read", "Grep", "Glob", "WebSearch", "WebFetch", "ReadLints", "TodoWrite", "AskQuestion", "GetMcpTools", "FetchMcpResource", "SemSearch")

var subagentTools = toolSet("Shell", "Grep", "Delete", "WebSearch", "WebFetch", "GenerateImage", "ReadLints", "EditNotebook", "TodoWrite", "StrReplace", "Write", "Read", "Glob", "GetMcpTools", "FetchMcpResource", "SwitchMode", "UpdateCurrentStep", "CallMcpTool", "SemSearch")

func subagentCatalog(kind string, all []provider.Tool) []provider.Tool {
	allowed := subagentTools
	switch strings.ToLower(kind) {
	case "explore", "cursor-guide", "ask":
		allowed = exploreTools
	}
	var out []provider.Tool
	for _, tool := range all {
		if allowed[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}

func withoutTool(all []provider.Tool, name string) []provider.Tool {
	var out []provider.Tool
	for _, tool := range all {
		if tool.Name != name {
			out = append(out, tool)
		}
	}
	return out
}

func modeFromID(id string) cursorpb.AgentMode {
	switch strings.ToLower(id) {
	case "ask":
		return cursorpb.AgentMode_AGENT_MODE_ASK
	case "plan":
		return cursorpb.AgentMode_AGENT_MODE_PLAN
	case "debug":
		return cursorpb.AgentMode_AGENT_MODE_DEBUG
	case "multitask":
		return cursorpb.AgentMode_AGENT_MODE_MULTITASK
	default:
		return cursorpb.AgentMode_AGENT_MODE_AGENT
	}
}

func mcpNote(defs []*cursorpb.McpToolDefinition) string {
	if len(defs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nMCP tools in this conversation. Call the tool by its name, or use CallMcpTool with server and toolName.\n")
	for _, def := range defs {
		if def == nil || def.GetToolName() == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s server=%s %s\n", def.GetToolName(), def.GetProviderIdentifier(), def.GetDescription())
	}
	return b.String()
}

func modeNote(mode cursorpb.AgentMode, ctx *cursorpb.RequestContext) string {
	var raw string
	switch mode {
	case cursorpb.AgentMode_AGENT_MODE_ASK:
		raw = askMode
	case cursorpb.AgentMode_AGENT_MODE_PLAN:
		raw = planMode
	case cursorpb.AgentMode_AGENT_MODE_DEBUG:
		raw = debugMode
	case cursorpb.AgentMode_AGENT_MODE_MULTITASK:
		raw = multitaskMode
	default:
		return ""
	}
	return "\n" + cleanMode(raw, ctx)
}

func (s *Session) roleNote() string {
	if s.run.GetSubagentTypeName() == "" {
		return ""
	}
	return "\n" + cleanMode(subagentMode, nil)
}

func cleanMode(raw string, ctx *cursorpb.RequestContext) string {
	debug := ctx.GetDebugModeConfig()
	replacer := strings.NewReplacer(
		"{{OPEN_FILES}}", "",
		"{{SELECTED_CONTEXT}}", "",
		"{{ACTION_CONTEXT}}", "",
		"{{TIMESTAMP}}", "",
		"{{USER_QUERY}}", "",
		"{{DEBUG_SERVER_ENDPOINT}}", debug.GetServerEndpoint(),
		"{{DEBUG_LOG_PATH}}", debug.GetLogPath(),
		"{{DEBUG_SESSION_ID}}", debug.GetSessionId(),
		"<user_query>\n</user_query>", "",
		"<user_query></user_query>", "",
	)
	return strings.TrimSpace(replacer.Replace(raw))
}

func toolSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

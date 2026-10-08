package agent

import (
	_ "embed"
	"fmt"
	"path"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
)

// prompt.md 取自 cursor-byok 的 prompt/cursor/agent/prompt.md。
//
//go:embed prompt.md
var basePrompt string

func systemPrompt(modelName string, ctx *cursorpb.RequestContext) string {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(basePrompt, "{{FAKE_MODEL_NAME}}", modelName))
	env := ctx.GetEnv()
	b.WriteString("\n<user_info>\n")
	if env.GetOsVersion() != "" {
		fmt.Fprintf(&b, "OS Version: %s\n", env.GetOsVersion())
	}
	if env.GetShell() != "" {
		fmt.Fprintf(&b, "Shell: %s\n", env.GetShell())
	}
	if paths := env.GetWorkspacePaths(); len(paths) > 0 {
		b.WriteString("Workspace Paths:\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if env.GetTimeZone() != "" {
		fmt.Fprintf(&b, "Time Zone: %s\n", env.GetTimeZone())
	}
	workspace := ""
	if paths := env.GetWorkspacePaths(); len(paths) > 0 {
		workspace = paths[0]
	}
	fmt.Fprintf(&b, "Is directory a git repo: %s\n", gitRepoLine(ctx, workspace))
	if folder := env.GetTerminalsFolder(); folder != "" {
		fmt.Fprintf(&b, "Terminals folder: %s\n", folder)
	}
	fmt.Fprintf(&b, "Today's date: %s\n", today(env.GetTimeZone()))
	b.WriteString("</user_info>\n")
	for _, repo := range ctx.GetGitRepos() {
		if strings.TrimSpace(repo.GetStatus()) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n<git_status>\nThis is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\n\nGit repo: %s\n\n```\n%s\n```\n</git_status>\n", repo.GetPath(), repo.GetStatus())
	}
	if folder := env.GetAgentTranscriptsFolder(); folder != "" {
		fmt.Fprintf(&b, "\n<agent_transcripts>\nAgent transcripts (past chats) live in %s. They have names like <uuid>.jsonl, cite parent chat transcripts to the user as [<title for chat <=6 words>](<uuid excluding .jsonl>). Don't discuss the folder structure.\n</agent_transcripts>\n", folder)
	}
	b.WriteString("\nUse absolute paths when calling tools. Read files before answering questions about them.\n")

	skillText := map[string]bool{}
	for _, skill := range ctx.GetAgentSkills() {
		if text := strings.TrimSpace(skill.GetContent()); text != "" {
			skillText[text] = true
		}
	}
	var global, fetched []string
	for _, rule := range ctx.GetRules() {
		text := strings.TrimSpace(rule.GetContent())
		if text == "" || skillText[text] || isSkillRule(rule) {
			continue
		}
		switch t := rule.GetType().GetType().(type) {
		case *cursorpb.CursorRuleType_Global:
			global = append(global, fmt.Sprintf("<rule path=%q>\n%s\n</rule>", rule.GetFullPath(), text))
		case *cursorpb.CursorRuleType_AgentFetched:
			fetched = append(fetched, fmt.Sprintf("- %s: %s", rule.GetFullPath(), t.AgentFetched.GetDescription()))
		}
	}
	if len(global) > 0 {
		b.WriteString("\n<rules>\nFollow these rules from the user's workspace:\n")
		b.WriteString(strings.Join(global, "\n"))
		b.WriteString("\n</rules>\n")
	}
	if len(fetched) > 0 {
		b.WriteString("\n<agent_requestable_rules>\nRead a rule file with the Read tool when its description is relevant:\n")
		b.WriteString(strings.Join(fetched, "\n"))
		b.WriteString("\n</agent_requestable_rules>\n")
	}
	var extra []string
	for _, rule := range ctx.GetNonFileRules() {
		text := strings.TrimSpace(rule.GetContent())
		if text == "" || skillText[text] || isSkillRule(rule) {
			continue
		}
		extra = append(extra, fmt.Sprintf("<rule>\n%s\n</rule>", text))
	}
	if cloud := strings.TrimSpace(ctx.GetCloudRule()); cloud != "" {
		extra = append(extra, fmt.Sprintf("<rule>\n%s\n</rule>", cloud))
	}
	if len(extra) > 0 {
		b.WriteString("\n<rules>\n")
		b.WriteString(strings.Join(extra, "\n"))
		b.WriteString("\n</rules>\n")
	}
	var skills []string
	for _, skill := range ctx.GetAgentSkills() {
		if skill.GetDisableModelInvocation() || strings.TrimSpace(skill.GetDescription()) == "" {
			continue
		}
		skills = append(skills, fmt.Sprintf("<agent_skill fullPath=%q>%s</agent_skill>", skill.GetFullPath(), xmlText(skill.GetDescription())))
	}
	if len(skills) > 0 {
		b.WriteString("\n<agent_skills>\n<available_skills>\n")
		b.WriteString(strings.Join(skills, "\n"))
		b.WriteString("\n</available_skills>\n</agent_skills>\n")
	}
	var agents []string
	for _, agent := range ctx.GetCustomSubagents() {
		if strings.TrimSpace(agent.GetName()) == "" {
			continue
		}
		agents = append(agents, fmt.Sprintf("<subagent name=%q>%s</subagent>", agent.GetName(), strings.TrimSpace(agent.GetDescription())))
	}
	if len(agents) > 0 {
		b.WriteString("\n<subagents>\n")
		b.WriteString(strings.Join(agents, "\n"))
		b.WriteString("\n</subagents>\n")
	}
	if mcp := mcpMetaTools(ctx.GetMcpMetaToolOptions()); mcp != "" {
		b.WriteString("\n")
		b.WriteString(mcp)
	}
	return b.String()
}

func gitRepoLine(ctx *cursorpb.RequestContext, workspace string) string {
	for _, repo := range ctx.GetGitRepos() {
		if workspace != "" && repo.GetPath() == workspace {
			return "Yes, at " + repo.GetPath()
		}
	}
	return "No"
}

func today(zone string) string {
	loc := time.Local
	if zone != "" {
		if parsed, err := time.LoadLocation(zone); err == nil {
			loc = parsed
		}
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func isSkillRule(rule *cursorpb.CursorRule) bool {
	return strings.EqualFold(path.Base(rule.GetFullPath()), "SKILL.md")
}

func xmlText(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func mcpMetaTools(options *cursorpb.McpMetaToolOptions) string {
	if options == nil {
		return ""
	}
	var servers []string
	for _, server := range options.GetMcpDescriptors() {
		if strings.TrimSpace(server.GetServerIdentifier()) == "" {
			continue
		}
		var tools []string
		for _, tool := range server.GetTools() {
			if strings.TrimSpace(tool.GetToolName()) == "" {
				continue
			}
			line := fmt.Sprintf("<mcp_tool name=%q>", tool.GetToolName())
			if desc := strings.TrimSpace(tool.GetDescription()); desc != "" {
				line += " " + xmlText(desc)
			}
			line += "</mcp_tool>"
			tools = append(tools, line)
		}
		if len(tools) == 0 {
			continue
		}
		name := server.GetServerName()
		if name == "" {
			name = server.GetServerIdentifier()
		}
		servers = append(servers, fmt.Sprintf("<mcp_meta_tool_server name=%q identifier=%q>\n%s\n</mcp_meta_tool_server>", name, server.GetServerIdentifier(), strings.Join(tools, "\n")))
	}
	if len(servers) == 0 {
		return ""
	}
	return "<mcp_meta_tools>\nThe following MCP tools are available. Call a listed tool directly with CallMcpTool without calling GetMcpTools first.\n" + strings.Join(servers, "\n") + "\n</mcp_meta_tools>\n"
}

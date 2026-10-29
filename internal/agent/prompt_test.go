package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
)

func TestRequestContextSectionsReachTheSystemPrompt(t *testing.T) {
	cloud := "be brief"
	desc := "probe & ping"
	text := systemPrompt("Mine", &cursorpb.RequestContext{
		Env: &cursorpb.RequestContextEnv{
			OsVersion:              "linux",
			WorkspacePaths:         []string{"/w"},
			TerminalsFolder:        "/tmp/terminals",
			AgentTranscriptsFolder: "/tmp/transcripts",
			TimeZone:               "UTC",
		},
		GitRepos: []*cursorpb.GitRepoInfo{{Path: "/w", Status: "## main"}},
		AgentSkills: []*cursorpb.AgentSkill{
			{FullPath: "/w/.cursor/skills/probe/SKILL.md", Description: desc},
			{FullPath: "/w/.cursor/skills/hidden/SKILL.md", Description: "hidden", DisableModelInvocation: true},
		},
		CustomSubagents: []*cursorpb.CustomSubagent{{Name: "reviewer", Description: "reviews diffs"}},
		CloudRule:       &cloud,
		Rules: []*cursorpb.CursorRule{{
			FullPath: "/w/.cursor/skills/probe/SKILL.md",
			Content:  "skill body",
			Type:     &cursorpb.CursorRuleType{Type: &cursorpb.CursorRuleType_Global{Global: &cursorpb.CursorRuleTypeGlobal{}}},
		}},
		McpMetaToolOptions: &cursorpb.McpMetaToolOptions{McpDescriptors: []*cursorpb.McpDescriptor{{
			ServerName:       "probe",
			ServerIdentifier: "project-0-workspace-probe",
			Tools:            []*cursorpb.McpToolDescriptor{{ToolName: "probe_ping", Description: &desc}},
		}}},
	})
	for _, want := range []string{
		"Is directory a git repo: Yes, at /w",
		"Terminals folder: /tmp/terminals",
		"/tmp/transcripts",
		"<rule>\nbe brief\n</rule>",
		`fullPath="/w/.cursor/skills/probe/SKILL.md"`,
		"probe &amp; ping",
		"<subagent name=\"reviewer\">reviews diffs</subagent>",
		`identifier="project-0-workspace-probe"`,
		`name="probe_ping"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "skill body") || strings.Contains(text, "hidden") {
		t.Fatal("skill file or disabled skill leaked into the prompt")
	}
	if strings.Contains(text, "Today's date:") || strings.Contains(text, "<git_status>") || strings.Contains(text, "## main") {
		t.Fatal("date or git status leaked into the system prompt")
	}
}

// 自定义模型不知道后台命令结束会唤醒它，就会在远端 nohup 完直接结束回合，嘴上答应「到时候汇报」。
func TestSystemPromptExplainsHowToBeWokenLater(t *testing.T) {
	text := systemPrompt("Mine", &cursorpb.RequestContext{})
	for _, want := range []string{"<background_work>", "block_until_ms: 0", "notify_on_output", "Nothing else wakes you", "sleep <seconds>", "message you again"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt is missing %q", want)
		}
	}
}

// aly-class models tend to roll back real DBs or delete business conditions then patch them back;
// keep a short production_safety guard in the system prompt.
func TestSystemPromptMentionsProductionSafety(t *testing.T) {
	text := systemPrompt("Mine", &cursorpb.RequestContext{})
	for _, want := range []string{"<production_safety>", "real databases", "production data", "wait for confirmation", "DROP"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt is missing %q", want)
		}
	}
}

func TestGitStatusStaysOnTheCurrentUserMessage(t *testing.T) {
	ctx := &cursorpb.RequestContext{GitRepos: []*cursorpb.GitRepoInfo{{Path: "/w", Status: "## main"}}}
	first := userTurn(&cursorpb.UserMessage{Text: "one"}, ctx)
	ctx.GitRepos[0].Status = "## main\n M a.go"
	second := userTurn(&cursorpb.UserMessage{Text: "two"}, ctx)
	if strings.Contains(first.Content, "M a.go") {
		t.Fatal("later git status rewrote the earlier user message")
	}
	if !strings.Contains(first.Content, "## main") || !strings.Contains(second.Content, "M a.go") {
		t.Fatalf("git status missing\nfirst:\n%s\nsecond:\n%s", first.Content, second.Content)
	}
	if strings.Contains(first.Content, "start of the conversation") {
		t.Fatal("git status still claims to be the conversation start")
	}
}

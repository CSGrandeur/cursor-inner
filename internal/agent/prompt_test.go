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
		"Today's date:",
		"<git_status>",
		"## main",
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
}

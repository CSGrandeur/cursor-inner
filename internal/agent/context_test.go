package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
)

func TestOpenFilesAndAttachedDiffReachThePrompt(t *testing.T) {
	line := int32(12)
	text := selectedContext(&cursorpb.SelectedContext{
		InvocationContext: &cursorpb.InvocationContext{Data: &cursorpb.InvocationContext_IdeState_{IdeState: &cursorpb.InvocationContext_IdeState{
			RecentlyViewedFiles: []*cursorpb.InvocationContext_IdeState_File{{Path: "old.go", TotalLines: 4}},
			VisibleFiles: []*cursorpb.InvocationContext_IdeState_File{
				{Path: "focus.go", TotalLines: 20, CursorPosition: &cursorpb.InvocationContext_IdeState_File_CursorPosition{Line: line}},
				{Path: "other.go", TotalLines: 3},
			},
		}}},
		GitDiff:     &cursorpb.SelectedGitDiff{Content: "diff --git a/focus.go"},
		ConsoleLogs: []*cursorpb.SelectedConsoleLog{{Level: "error", Message: "boom"}},
	})
	for _, want := range []string{
		"Recently viewed files",
		"old.go (total lines: 4)",
		"focus.go (currently focused file, cursor is on line 12, total lines: 20)",
		"other.go (total lines: 3)",
		"<git_diff>\ndiff --git a/focus.go\n</git_diff>",
		"<console level=\"error\">\nboom\n</console>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

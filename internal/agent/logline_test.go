package agent

import "testing"

func TestFormatToolUsesListsNamesInCallOrder(t *testing.T) {
	got := formatToolUses([]string{"Read", "Grep", "StrReplace"}, map[string]int{"Read": 3, "Grep": 2, "StrReplace": 1})
	if got != "Read×3 Grep×2 StrReplace×1" {
		t.Fatal(got)
	}
	if formatToolUses(nil, nil) != "" {
		t.Fatal("empty")
	}
}

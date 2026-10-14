package program

import (
	"strings"
	"testing"
)

func TestRestoreBalloonNamesClosedApps(t *testing.T) {
	b := restoreBalloon(true, []string{"grok", "codex"})
	if !strings.Contains(b.Title, "Grok Bot 和 Codex") || b.Page != "restore" || strings.ContainsAny(b.Title+b.Text, "「」") {
		t.Fatalf("%+v", b)
	}
	if b := restoreBalloon(false, []string{"grok"}); !strings.Contains(b.Title, "Grok Bot") || strings.Contains(b.Title, "and") {
		t.Fatalf("%+v", b)
	}
}

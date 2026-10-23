package agent

import (
	"strings"
	"testing"

	"cursor-inner/internal/provider"
)

func TestLoopTrackerWarnThenHardStop(t *testing.T) {
	tr := newLoopTracker()
	call := provider.ToolCall{ID: "c1", Name: "Read", Arguments: `{"path":"/w/a.go"}`}
	for i := 1; i <= 2; i++ {
		if stop := tr.observe([]provider.ToolCall{call}); stop {
			t.Fatalf("stop too early at %d", i)
		}
		if tr.takeReminder() != "" {
			t.Fatalf("no reminder before warn threshold, at %d", i)
		}
	}
	if stop := tr.observe([]provider.ToolCall{call}); stop {
		t.Fatal("should not stop on 3rd under warn mode")
	}
	warn := tr.takeReminder()
	if warn == "" || !strings.Contains(warn, "Read") {
		t.Fatalf("want identical-call reminder on 3rd, got %q", warn)
	}
	// Soft threshold (old stop-at-5): warn-mode continues; one reminder per streak.
	for i := 4; i <= 7; i++ {
		if stop := tr.observe([]provider.ToolCall{call}); stop {
			t.Fatalf("should not soft-stop at %d under warn mode", i)
		}
		if rem := tr.takeReminder(); rem != "" {
			t.Fatalf("one reminder per identical streak; got another at %d: %q", i, rem)
		}
	}
	if stop := tr.observe([]provider.ToolCall{call}); !stop {
		t.Fatal("want hard stop on 8th consecutive identical call")
	}
}

func TestLoopTrackerComputerStreakReminder(t *testing.T) {
	tr := newLoopTracker()
	seq := []provider.ToolCall{
		{ID: "1", Name: "computer_screenshot", Arguments: `{}`},
		{ID: "2", Name: "computer_type", Arguments: `{"text":"ls"}`},
		{ID: "3", Name: "computer_click", Arguments: `{"x":10,"y":20}`},
	}
	if stop := tr.observe(seq[:2]); stop {
		t.Fatal("unexpected stop")
	}
	if rem := tr.takeReminder(); rem != "" {
		t.Fatalf("no computer reminder before streak 3, got %q", rem)
	}
	if stop := tr.observe(seq[2:]); stop {
		t.Fatal("computer streak warns, does not stop")
	}
	rem := tr.takeReminder()
	if rem == "" || !strings.Contains(rem, "Computer Use") {
		t.Fatalf("want computer streak reminder, got %q", rem)
	}
	if !strings.Contains(rem, "Shell") {
		t.Fatalf("reminder should point at Shell/Read/Write, got %q", rem)
	}
	// Broken by a non-computer tool; streak resets.
	if stop := tr.observe([]provider.ToolCall{{ID: "4", Name: "Shell", Arguments: `{"command":"ls"}`}}); stop {
		t.Fatal("unexpected stop on Shell")
	}
	if rem := tr.takeReminder(); rem != "" {
		t.Fatalf("Shell must clear computer streak without reminder, got %q", rem)
	}
	if stop := tr.observe([]provider.ToolCall{
		{ID: "5", Name: "computer_screenshot", Arguments: `{"n":1}`},
		{ID: "6", Name: "computer_screenshot", Arguments: `{"n":2}`},
	}); stop {
		t.Fatal("unexpected stop")
	}
	if rem := tr.takeReminder(); rem != "" {
		t.Fatalf("streak reset; 2 computer calls must not remind, got %q", rem)
	}
}

func TestAppendLoopReminder(t *testing.T) {
	if got := appendLoopReminder("ok", ""); got != "ok" {
		t.Fatalf("empty reminder: %q", got)
	}
	got := appendLoopReminder("ok", "System: nudge")
	if !strings.HasSuffix(got, "System: nudge") || !strings.Contains(got, "ok") {
		t.Fatalf("append: %q", got)
	}
	if got := appendLoopReminder("", "System: only"); got != "System: only" {
		t.Fatalf("empty text: %q", got)
	}
}

func TestLoopTrackerTakeReminderForCall(t *testing.T) {
	tr := newLoopTracker()
	call := provider.ToolCall{ID: "same", Name: "Shell", Arguments: `{"command":"pwd"}`}
	for i := 0; i < 3; i++ {
		tr.observe([]provider.ToolCall{call})
	}
	if rem := tr.takeReminderFor("other"); rem != "" {
		t.Fatalf("wrong call id must not take, got %q", rem)
	}
	rem := tr.takeReminderFor("same")
	if rem == "" {
		t.Fatal("want reminder for matching call id")
	}
	if rem2 := tr.takeReminderFor("same"); rem2 != "" {
		t.Fatalf("reminder is one-shot, got %q", rem2)
	}
}

func TestLoopTrackerContentChant(t *testing.T) {
	tr := newLoopTracker()
	tr.resetText()
	fired := false
	for i := 0; i < 60 && !fired; i++ {
		fired = tr.feedText("I will now fix the bug in the file. ")
	}
	if !fired {
		t.Fatal("want content chant detected")
	}
	tr2 := newLoopTracker()
	tr2.resetText()
	if tr2.feedText("Here is a concise and varied explanation of the change.") {
		t.Fatal("normal prose must not be flagged")
	}
}

func TestLoopTrackerDifferentArgsOk(t *testing.T) {
	tr := newLoopTracker()
	for i := 0; i < 5; i++ {
		call := provider.ToolCall{Name: "Read", Arguments: `{"path":"/w/` + string(rune('a'+i)) + `.go"}`}
		if stop := tr.observe([]provider.ToolCall{call}); stop {
			t.Fatalf("stopped at %d", i)
		}
	}
}

func TestIsComputerTool(t *testing.T) {
	for _, name := range []string{"computer_screenshot", "computer_type", "Computer_Click", "mcp_computer_use"} {
		if !isComputerTool(name) {
			t.Fatalf("want computer tool: %s", name)
		}
	}
	for _, name := range []string{"Shell", "Read", "CallMcpTool", "browser_click"} {
		if isComputerTool(name) {
			t.Fatalf("not computer tool: %s", name)
		}
	}
}

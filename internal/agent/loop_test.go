package agent

import (
	"testing"

	"cursor-inner/internal/provider"
)

func TestLoopTrackerWarnThenStop(t *testing.T) {
	tr := newLoopTracker()
	call := provider.ToolCall{Name: "Read", Arguments: `{"path":"/w/a.go"}`}
	for i := 1; i <= 2; i++ {
		if _, stop := tr.observe([]provider.ToolCall{call}); stop {
			t.Fatalf("stop too early at %d", i)
		}
	}
	warn, stop := tr.observe([]provider.ToolCall{call})
	if stop || warn == "" {
		t.Fatalf("want warn on 3rd, got warn=%q stop=%v", warn, stop)
	}
	if _, stop := tr.observe([]provider.ToolCall{call}); stop {
		t.Fatal("should not stop on 4th (consecutive threshold is 5)")
	}
	if _, stop := tr.observe([]provider.ToolCall{call}); !stop {
		t.Fatal("want stop on 5th")
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
		if _, stop := tr.observe([]provider.ToolCall{call}); stop {
			t.Fatalf("stopped at %d", i)
		}
	}
}

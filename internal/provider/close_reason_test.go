package provider

import (
	"context"
	"strings"
	"testing"
)

func TestCloseReasonCanceledIsStoppedNotInterrupted(t *testing.T) {
	got := CloseReason(context.Canceled)
	if got != "已停止" {
		t.Fatalf("cancel close reason = %q, want 已停止", got)
	}
	if strings.Contains(got, "Operation interrupted") {
		t.Fatalf("cancel must not look like a generic interrupt: %q", got)
	}
}

func TestCloseReasonUpstreamKeepsExplain(t *testing.T) {
	err := &APIError{Kind: KindServer, Status: 503, Detail: "Master scheduling failed (RESOURCE_EXHAUSTED)"}
	got := CloseReason(err)
	want := Explain(err)
	if got != want {
		t.Fatalf("CloseReason = %q, want Explain %q", got, want)
	}
	if !strings.Contains(got, "503") || !strings.Contains(got, "RESOURCE_EXHAUSTED") {
		t.Fatalf("upstream close reason should stay readable: %q", got)
	}
	if strings.Contains(got, "Operation interrupted") {
		t.Fatalf("upstream error must not collapse to Operation interrupted: %q", got)
	}
}

func TestCloseReasonNilKeepsLegacyInterrupt(t *testing.T) {
	if got := CloseReason(nil); got != interruptedTurn {
		t.Fatalf("nil err CloseReason = %q, want %q", got, interruptedTurn)
	}
}

func TestCloseDanglingWithCanceledReason(t *testing.T) {
	out := CloseDanglingWith([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "Shell"}}},
		{Role: "tool", ToolCallID: "a", Content: "ok"},
	}, CloseReason(context.Canceled))
	if len(out) == 0 || out[len(out)-1].Role != "assistant" || out[len(out)-1].Content != "已停止" {
		t.Fatalf("%+v", out)
	}
}

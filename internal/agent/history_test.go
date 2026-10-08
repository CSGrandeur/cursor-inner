package agent

import (
	"testing"

	"cursor-inner/internal/provider"
)

func TestHistoryStoresCursorBlobs(t *testing.T) {
	h := NewHistory(t.TempDir())
	messages := []provider.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}}
	if err := h.Save("c", messages); err != nil {
		t.Fatal(err)
	}
	got := h.Load("c")
	if len(got) != 2 || got[0].Content != "one" || got[1].Content != "two" {
		t.Fatalf("%+v", got)
	}
	state := h.LoadState("c")
	if state == nil || len(state.GetTurns()) != 1 || len(state.GetTurns()[0]) != 32 {
		t.Fatalf("%v", state)
	}
}

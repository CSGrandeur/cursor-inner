package agent

import (
	"testing"
	"time"

	"cursor-inner/internal/cursorpb"
)

func TestHubOwnsOnlyLocalSessions(t *testing.T) {
	h := New(nil, nil)
	h.slots["local"] = &slot{session: &Session{}, born: time.Now()}
	h.slots["official"] = &slot{decided: true, born: time.Now()}
	h.finished["done"] = &finished{session: &Session{}, at: time.Now()}
	for id, want := range map[string]bool{"local": true, "done": true, "official": false, "unknown": false, "": false} {
		if got := h.Owns(id); got != want {
			t.Errorf("Owns(%q)=%v, want %v", id, got, want)
		}
	}
}

func TestHubKnowsConversation(t *testing.T) {
	dir := t.TempDir()
	hist := NewHistory(dir)
	h := New(nil, hist)
	cid := "conv-local"
	h.byConv[cid] = &finished{session: &Session{run: &cursorpb.AgentRunRequest{ConversationId: &cid}}, at: time.Now()}
	if !h.KnowsConversation(cid) {
		t.Fatal("byConv should be known")
	}
	if h.KnowsConversation("unknown") {
		t.Fatal("unknown must be false")
	}
	if h.KnowsConversation("") {
		t.Fatal("empty must be false")
	}
	other := "conv-disk"
	if err := hist.SaveState(other, &cursorpb.ConversationStateStructure{}); err != nil {
		t.Fatal(err)
	}
	if !h.KnowsConversation(other) {
		t.Fatal("history LoadState should be known")
	}
}

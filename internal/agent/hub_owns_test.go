package agent

import (
	"testing"
	"time"
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

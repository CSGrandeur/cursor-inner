package takeover

import (
	"slices"
	"testing"
)

func TestRestoreListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := PendingRestore(dir); len(got) != 0 {
		t.Fatalf("empty dir: %v", got)
	}
	NoteRestore(dir, "codex")
	NoteRestore(dir, "grok", "codex")
	if got := PendingRestore(dir); !slices.Equal(got, []string{"grok", "codex"}) {
		t.Fatalf("got %v", got)
	}
	ClearRestore(dir, "grok")
	if got := PendingRestore(dir); !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("got %v", got)
	}
	ClearRestore(dir)
	if got := PendingRestore(dir); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

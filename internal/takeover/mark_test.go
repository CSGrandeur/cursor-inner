package takeover

import "testing"

func TestMarkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if Marked(dir) {
		t.Fatal("empty dir is marked")
	}
	if err := Mark(dir); err != nil {
		t.Fatal(err)
	}
	if !Marked(dir) {
		t.Fatal("not marked")
	}
	Unmark(dir)
	if Marked(dir) {
		t.Fatal("still marked")
	}
	Unmark(dir)
}

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

func TestMarkURLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if MarkedProxy(dir) != "" {
		t.Fatal("empty")
	}
	if err := Mark(dir); err != nil {
		t.Fatal(err)
	}
	if MarkedProxy(dir) != "" {
		t.Fatal("legacy 1 must be empty proxy")
	}
	if err := MarkURL(dir, "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if got := MarkedProxy(dir); got != "http://127.0.0.1:9" {
		t.Fatalf("got %q", got)
	}
}

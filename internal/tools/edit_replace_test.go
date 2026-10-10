package tools

import "testing"

func TestWhitespaceReplaceIgnoresIndent(t *testing.T) {
	before := "func main() {\n\tfoo := 1\n\tbar := 2\n}\n"
	after, err := replaceString(args{
		"old_string": "foo := 1\nbar := 2",
		"new_string": "foo := 3\nbar := 4",
	}, before)
	if err != nil {
		t.Fatal(err)
	}
	want := "func main() {\n\tfoo := 3\n\tbar := 4\n}\n"
	if after != want {
		t.Fatalf("got %q want %q", after, want)
	}
}

func TestFuzzyReplaceUniqueNearMatch(t *testing.T) {
	before := "package main\n\nfunc hello() {\n\tprintln(\"hello world\")\n}\n"
	after, err := replaceString(args{
		"old_string": "func hello() {\n\tprintln(\"hello word\")\n}",
		"new_string": "func hello() {\n\tprintln(\"hi\")\n}",
	}, before)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(after, "println(\"hi\")") {
		t.Fatalf("got %q", after)
	}
}

func TestExactStillPreferred(t *testing.T) {
	before := "aaa\nbbb\nccc\n"
	after, err := replaceString(args{
		"old_string": "bbb",
		"new_string": "BBB",
	}, before)
	if err != nil {
		t.Fatal(err)
	}
	if after != "aaa\nBBB\nccc\n" {
		t.Fatal(after)
	}
}

func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

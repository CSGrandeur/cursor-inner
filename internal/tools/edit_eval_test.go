package tools

import (
	"testing"
)

// editCase 是一条弱模型常写错的 StrReplace。want 为空表示必须拒绝（不唯一或根本不存在）。
type editCase struct {
	name, before, old, next, want string
}

var editCases = []editCase{
	{"exact", "a\nfoo := 1\nb\n", "foo := 1", "foo := 2", "a\nfoo := 2\nb\n"},
	{"indent-tabs-vs-spaces", "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n", "    if x {\n        return 1\n    }", "    if x {\n        return 2\n    }", "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n"},
	{"inner-spaces", "x  =  compute(a,  b)\n", "x = compute(a, b)", "x = compute(a, c)", "x = compute(a, c)\n"},
	{"typo-fuzzy", "package main\n\nfunc hello() {\n\tprintln(\"hello world\")\n\treturn\n}\n", "func hello() {\n\tprintln(\"hello word\")\n\treturn\n}", "func hello() {\n\tprintln(\"hi\")\n\treturn\n}", "package main\n\nfunc hello() {\n\tprintln(\"hi\")\n\treturn\n}\n"},
	{"crlf-file", "a\r\nfoo := 1\r\nb\r\n", "foo := 1", "foo := 2", "a\r\nfoo := 2\r\nb\r\n"},
	{"crlf-file-multiline", "a\r\nfoo := 1\r\nbar := 2\r\nb\r\n", "foo := 1\nbar := 2", "foo := 3\nbar := 4", "a\r\nfoo := 3\r\nbar := 4\r\nb\r\n"},
	{"read-line-numbers-pipe", "package p\n\nfunc a() int {\n\treturn 1\n}\n", "     3|func a() int {\n     4|\treturn 1\n     5|}", "     3|func a() int {\n     4|\treturn 2\n     5|}", "package p\n\nfunc a() int {\n\treturn 2\n}\n"},
	{"read-line-numbers-L", "one\ntwo\nthree\n", "L2:two\nL3:three", "L2:TWO\nL3:three", "one\nTWO\nthree\n"},
	{"trailing-newline-in-old", "x\ny\n", "y\n\n", "z\n\n", "x\nz\n"},
	{"trailing-spaces-in-file", "keep\nvalue = 1   \nend\n", "value = 1", "value = 2", "keep\nvalue = 2   \nend\n"},
	{"smart-quotes", "msg := \"don't stop\"\nother := 1\n", "msg := “don’t stop”", "msg := \"go\"", "msg := \"go\"\nother := 1\n"},
	{"ambiguous-must-fail", "x := 1\ny := 2\nx := 1\n", "x  := 1", "x := 9", ""},
	{"absent-must-fail", "alpha\nbeta\n", "completely different text here", "zzz", ""},
}

func TestEditEvalCorpus(t *testing.T) {
	pass := 0
	for _, c := range editCases {
		got, err := replaceString(args{"old_string": c.old, "new_string": c.next}, c.before)
		ok := false
		if c.want == "" {
			ok = err != nil
		} else {
			ok = err == nil && got == c.want
		}
		if ok {
			pass++
		} else {
			t.Logf("MISS %-26s err=%v got=%q", c.name, err, got)
		}
	}
	t.Logf("edit eval: %d/%d", pass, len(editCases))
	if pass < editMinPass {
		t.Fatalf("edit eval regressed: %d/%d < %d", pass, len(editCases), editMinPass)
	}
}

var editMinPass = 13

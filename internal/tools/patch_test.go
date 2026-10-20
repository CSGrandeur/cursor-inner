package tools

import (
	"strings"
	"testing"

	"cursor-inner/internal/provider"
)

type patchCase struct {
	name, before, patch, want string // want 为空表示必须报错
}

var patchCases = []patchCase{
	{"single-hunk", "package p\n\nfunc a() int {\n\treturn 1\n}\n",
		"*** Begin Patch\n*** Update File: a.go\n@@ func a() int {\n func a() int {\n-\treturn 1\n+\treturn 2\n }\n*** End Patch",
		"package p\n\nfunc a() int {\n\treturn 2\n}\n"},
	{"two-hunks", "a\nb\nc\nd\ne\nf\n",
		"*** Begin Patch\n*** Update File: x\n@@\n a\n-b\n+B\n c\n@@\n e\n-f\n+F\n*** End Patch",
		"a\nB\nc\nd\ne\nF\n"},
	{"context-missing-space", "x := 1\ny := 2\nz := 3\n",
		"*** Begin Patch\n*** Update File: x\n@@\nx := 1\n-y := 2\n+y := 20\nz := 3\n*** End Patch",
		"x := 1\ny := 20\nz := 3\n"},
	{"indent-drift", "func f() {\n\tif ok {\n\t\tgo()\n\t}\n}\n",
		"*** Begin Patch\n*** Update File: f.go\n@@\n     if ok {\n-        go()\n+        stop()\n     }\n*** End Patch",
		"func f() {\n\tif ok {\n\t\tstop()\n\t}\n}\n"},
	{"crlf", "a\r\nb\r\nc\r\n",
		"*** Begin Patch\n*** Update File: x\n@@\n a\n-b\n+B\n c\n*** End Patch",
		"a\r\nB\r\nc\r\n"},
	{"pure-insert-after-anchor", "import x\n\nfunc main() {}\n",
		"*** Begin Patch\n*** Update File: x\n@@ import x\n+import y\n*** End Patch",
		"import x\nimport y\n\nfunc main() {}\n"},
	{"no-begin-end", "one\ntwo\n",
		"*** Update File: x\n@@\n-two\n+2\n",
		"one\n2\n"},
	{"stale-context-must-fail", "alpha\nbeta\n",
		"*** Begin Patch\n*** Update File: x\n@@\n totally\n-unrelated\n+lines\n*** End Patch",
		""},
}

func TestPatchEvalCorpus(t *testing.T) {
	pass := 0
	for _, c := range patchCases {
		ops, err := parsePatch(c.patch)
		var got string
		if err == nil {
			got, err = applyPatchHunks(c.before, ops[0].hunks)
		}
		ok := (c.want == "" && err != nil) || (c.want != "" && err == nil && got == c.want)
		if ok {
			pass++
		} else {
			t.Logf("MISS %-26s err=%v got=%q", c.name, err, got)
		}
	}
	t.Logf("patch eval: %d/%d", pass, len(patchCases))
	if pass != len(patchCases) {
		t.Fatalf("patch eval %d/%d", pass, len(patchCases))
	}
}

func TestPatchCallRewrites(t *testing.T) {
	add, ops, err := patchCall(provider.ToolCall{ID: "1", Name: "apply_patch"}, "*** Begin Patch\n*** Add File: n.txt\n+hello\n+world\n*** End Patch")
	if err != nil || ops != nil || add.Name != "Write" || !strings.Contains(add.Arguments, `"contents":"hello\nworld\n"`) {
		t.Fatalf("%+v %v", add, err)
	}
	if _, ops, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n*** Update File: b\n@@\n-x\n+y\n*** End Patch"); err != nil || len(ops) != 2 {
		t.Fatalf("multi-file patch goes to the multi-file flow: %v %v", ops, err)
	}
	del, ops, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Delete File: a\n*** End Patch")
	if err != nil || ops != nil || del.Name != "Delete" || del.Arguments != `{"path":"a"}` {
		t.Fatalf("single delete becomes the Delete tool: %+v %v", del, err)
	}
	if _, ops, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** End Patch"); err != nil || len(ops) != 1 || ops[0].move != "b" {
		t.Fatalf("a move goes to the multi-file flow: %v %v", ops, err)
	}
	if _, _, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Update File: a\n*** End Patch"); err == nil {
		t.Fatal("an update without hunks or move must fail")
	}
	text, ok := shellPatch("apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n*** End Patch\nEOF")
	if !ok || !strings.HasPrefix(text, patchBegin) || !strings.HasSuffix(text, patchEnd) {
		t.Fatalf("%q %v", text, ok)
	}
	if _, ok := shellPatch("cd /w && apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n*** End Patch\nEOF"); !ok {
		t.Fatal("cd prefix")
	}
	if _, ok := shellPatch("echo '*** Begin Patch' > notes.txt"); ok {
		t.Fatal("not an apply_patch command")
	}
}

func TestAnchorPicksTheRightDuplicate(t *testing.T) {
	before := "func a() {\n\treturn nil\n}\n\nfunc b() {\n\treturn nil\n}\n"
	ops, err := parsePatch("*** Begin Patch\n*** Update File: x.go\n@@ func b() {\n-\treturn nil\n+\treturn errB\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	got, err := applyPatchHunks(before, ops[0].hunks)
	if err != nil || got != "func a() {\n\treturn nil\n}\n\nfunc b() {\n\treturn errB\n}\n" {
		t.Fatalf("%q %v", got, err)
	}
	// 没有定位行时仍按“不唯一”报错，不去猜。
	ops, _ = parsePatch("*** Begin Patch\n*** Update File: x.go\n@@\n-\treturn nil\n+\treturn errB\n*** End Patch")
	if _, err := applyPatchHunks(before, ops[0].hunks); err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ambiguous hunk without anchor must fail: %v", err)
	}
}

type multiCase struct {
	name  string
	files map[string]string
	patch string
	want  map[string]*string // nil 值表示被删；want 为 nil 表示必须报错
	sum   string
}

func str(s string) *string { return &s }

var multiCases = []multiCase{
	{"update-add-delete", map[string]string{"a.go": "x\ny\n", "old.txt": "bye\n"},
		"*** Begin Patch\n*** Update File: a.go\n@@\n x\n-y\n+z\n*** Add File: dir/new.go\n+package dir\n*** Delete File: old.txt\n*** End Patch",
		map[string]*string{"a.go": str("x\nz\n"), "dir/new.go": str("package dir\n"), "old.txt": nil},
		"Success. Updated the following files:\nM a.go\nA dir/new.go\nD old.txt"},
	{"move-with-edit", map[string]string{"src/a.go": "package a\n\nfunc A() {}\n"},
		"*** Begin Patch\n*** Update File: src/a.go\n*** Move to: pkg/a.go\n@@\n-func A() {}\n+func A() int { return 1 }\n*** End Patch",
		map[string]*string{"pkg/a.go": str("package a\n\nfunc A() int { return 1 }\n"), "src/a.go": nil},
		"Success. Updated the following files:\nM src/a.go -> pkg/a.go"},
	{"pure-rename", map[string]string{"a.txt": "keep\n"},
		"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End Patch",
		map[string]*string{"b.txt": str("keep\n"), "a.txt": nil}, ""},
	{"same-file-twice", map[string]string{"a": "1\n2\n3\n"},
		"*** Begin Patch\n*** Update File: a\n@@\n-1\n+one\n*** Update File: a\n@@\n-3\n+three\n*** End Patch",
		map[string]*string{"a": str("one\n2\nthree\n")}, ""},
	{"add-then-update", map[string]string{},
		"*** Begin Patch\n*** Add File: n\n+a\n+b\n*** Update File: n\n@@\n-b\n+B\n*** End Patch",
		map[string]*string{"n": str("a\nB\n")}, ""},
	{"second-file-miss-changes-nothing", map[string]string{"a": "x\n", "b": "y\n"},
		"*** Begin Patch\n*** Update File: a\n@@\n-x\n+X\n*** Update File: b\n@@\n-nothing like this\n+Y\n*** End Patch",
		nil, ""},
	{"update-missing-file", map[string]string{},
		"*** Begin Patch\n*** Update File: ghost.go\n@@\n-a\n+b\n*** End Patch", nil, ""},
	{"delete-missing-file", map[string]string{"a": "x\n"},
		"*** Begin Patch\n*** Update File: a\n@@\n-x\n+X\n*** Delete File: ghost\n*** End Patch", nil, ""},
	{"update-after-delete", map[string]string{"a": "x\n"},
		"*** Begin Patch\n*** Delete File: a\n*** Update File: a\n@@\n-x\n+X\n*** End Patch", nil, ""},
}

func TestApplyPatchFilesCorpus(t *testing.T) {
	for _, c := range multiCases {
		got, sum, err := ApplyPatchFiles(c.patch, c.files)
		if c.want == nil {
			if err == nil || !strings.Contains(err.Error(), "no files were changed") {
				t.Errorf("%s: want an error that changes nothing, got %v %v", c.name, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: changed %v want %v", c.name, keys(got), keys(c.want))
		}
		for path, w := range c.want {
			g, ok := got[path]
			if !ok || (w == nil) != (g == nil) || (w != nil && *w != *g) {
				t.Errorf("%s: %s = %v want %v", c.name, path, deref(g), deref(w))
			}
		}
		if c.sum != "" && sum != c.sum {
			t.Errorf("%s: summary %q", c.name, sum)
		}
	}
}

func keys(m map[string]*string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return "<deleted>"
	}
	return *s
}

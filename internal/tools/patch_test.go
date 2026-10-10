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
	add, err := patchCall(provider.ToolCall{ID: "1", Name: "apply_patch"}, "*** Begin Patch\n*** Add File: n.txt\n+hello\n+world\n*** End Patch")
	if err != nil || add.Name != "Write" || !strings.Contains(add.Arguments, `"contents":"hello\nworld\n"`) {
		t.Fatalf("%+v %v", add, err)
	}
	if _, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n*** Update File: b\n@@\n-x\n+y\n*** End Patch"); err == nil {
		t.Fatal("multi-file patch must ask to split")
	}
	if _, err := patchCall(provider.ToolCall{}, "*** Begin Patch\n*** Delete File: a\n*** End Patch"); err == nil {
		t.Fatal("delete must point to the Delete tool")
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

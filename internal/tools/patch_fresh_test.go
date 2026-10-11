package tools

import (
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// 新建的文件（Add、Move 的新路径、Write 到不存在的路径）卡片不带 before_full_file_content：
// Cursor 拒绝/撤销时按它是否存在区分「恢复原内容」和「删掉新文件」，带空串会留下 0 字节文件（2026-10-11 隔离 Cursor 实测）。
func TestPatchCardsMarkNewFiles(t *testing.T) {
	call := provider.ToolCall{ID: "p1", Name: "apply_patch", Arguments: patchArgs(t, flowPatch)}
	_, ui, pending, err := Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	cards := map[string]*cursorpb.EditSuccess{}
	collect := func(deltas []*cursorpb.AgentServerMessage) {
		for _, d := range deltas {
			done := d.GetInteractionUpdate().GetToolCallCompleted()
			if s := done.GetToolCall().GetEditToolCall().GetResult().GetSuccess(); s != nil {
				cards[s.GetPath()] = s
			}
		}
	}
	replies := []*cursorpb.ExecClientMessage{
		readResult("old\n", false), readResult("bye\n", false), readResult("d\n", false),
		writeOK("a.go"), writeOK("b.go"), deleteOK("c.go"), writeOK("e.go"), deleteOK("d.go"),
	}
	for i, r := range replies {
		_, deltas, text, isErr, _ := pending.Feed(r, ui)
		if isErr {
			t.Fatalf("step %d: %q", i, text)
		}
		collect(deltas)
	}
	if s := ui.GetEditToolCall().GetResult().GetSuccess(); s.BeforeFullFileContent == nil || s.GetBeforeFullFileContent() != "old\n" {
		t.Fatalf("updated file must keep its before content: %v", s)
	}
	for _, path := range []string{"b.go", "e.go"} {
		s := cards[path]
		if s == nil {
			t.Fatalf("no card for %s: %v", path, cards)
		}
		if s.BeforeFullFileContent != nil {
			t.Fatalf("%s is new, card must not carry before_full_file_content (got %q)", path, s.GetBeforeFullFileContent())
		}
		if s.GetLinesRemoved() != 0 || s.GetLinesAdded() != 1 {
			t.Fatalf("%s lines +%d -%d", path, s.GetLinesAdded(), s.GetLinesRemoved())
		}
	}
}

// Move 到一个已存在的路径：卡片的 before 是目标原来的内容，不是旧文件，也不算新文件。
func TestPatchMoveOntoExistingFileKeepsTargetBefore(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: d.go\n*** Move to: e.go\n@@\n-d\n+e\n*** End Patch"
	_, ui, pending, err := Request(1, provider.ToolCall{ID: "m1", Name: "apply_patch", Arguments: patchArgs(t, patch)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, isErr, _ := pending.Feed(readResult("d\n", false), ui)
	if isErr {
		t.Fatal("read d")
	}
	st := pending.patch
	st.files["e.go"] = patchFile{exists: true, text: "target\n"}
	planned, err := planPatch(st.ops, st.files)
	if err != nil {
		t.Fatal(err)
	}
	if planned[0].path != "e.go" || planned[0].fresh || planned[0].before != "target\n" || planned[0].after != "e\n" {
		t.Fatalf("move write step %+v", planned[0])
	}
	if planned[1].kind != stepDelete || planned[1].path != "d.go" || planned[1].before != "d\n" {
		t.Fatalf("move delete step %+v", planned[1])
	}
}

func TestWriteToMissingFileHasNoBeforeContent(t *testing.T) {
	_, ui, pending, err := Request(4, provider.ToolCall{ID: "w1", Name: "Write", Arguments: `{"path":"/w/b.go","content":"package b\n"}`})
	if err != nil {
		t.Fatal(err)
	}
	pending.Advance(notFound("/w/b.go"), ui)
	pending.Advance(writeOK("/w/b.go"), ui)
	if s := ui.GetEditToolCall().GetResult().GetSuccess(); s == nil || s.BeforeFullFileContent != nil {
		t.Fatalf("new file card %v", ui.GetEditToolCall().GetResult())
	}

	_, ui, pending, err = Request(5, provider.ToolCall{ID: "w2", Name: "Write", Arguments: `{"path":"/w/e.go","content":"x\n"}`})
	if err != nil {
		t.Fatal(err)
	}
	pending.Advance(readResult("", false), ui)
	pending.Advance(writeOK("/w/e.go"), ui)
	if s := ui.GetEditToolCall().GetResult().GetSuccess(); s == nil || s.BeforeFullFileContent == nil || s.GetBeforeFullFileContent() != "" {
		t.Fatalf("existing empty file must keep an empty before: %v", ui.GetEditToolCall().GetResult())
	}
}

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func deleteOK(path string) *cursorpb.ExecClientMessage {
	return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_DeleteResult{DeleteResult: &cursorpb.DeleteResult{
		Result: &cursorpb.DeleteResult_Success{Success: &cursorpb.DeleteSuccess{Path: path}},
	}}}
}

func notFound(path string) *cursorpb.ExecClientMessage {
	return &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_ReadResult{ReadResult: &cursorpb.ReadResult{
		Result: &cursorpb.ReadResult_FileNotFound{FileNotFound: &cursorpb.ReadFileNotFound{Path: path}},
	}}}
}

func patchArgs(t *testing.T, patch string) string {
	raw, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const flowPatch = "*** Begin Patch\n" +
	"*** Update File: a.go\n@@\n-old\n+new\n" +
	"*** Add File: b.go\n+package b\n" +
	"*** Delete File: c.go\n" +
	"*** Update File: d.go\n*** Move to: e.go\n@@\n-d\n+e\n" +
	"*** End Patch"

// 多文件补丁：先读完 a、c、d，算好再依次写 a、写 b、删 c、写 e、删 d；
// 第一张卡片沿用模型的调用 id，其余每步一张卡片，各自的 Write/Delete 带对应的卡片 id。
func TestMultiFilePatchReadsAllThenWritesInOrder(t *testing.T) {
	call := provider.ToolCall{ID: "p1", Name: "apply_patch", Arguments: patchArgs(t, flowPatch)}
	exec, ui, pending, err := Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetReadArgs().GetPath() != "a.go" || ui.GetToolCallId() != "p1" || ui.GetEditToolCall().GetArgs().GetPath() != "a.go" {
		t.Fatalf("first step %v / %v", exec, ui)
	}
	var reads []string
	reads = append(reads, exec.GetReadArgs().GetPath())
	feed := func(msg *cursorpb.ExecClientMessage) (*cursorpb.ExecServerMessage, []*cursorpb.AgentServerMessage, string, bool, bool) {
		next, deltas, text, isErr, done := pending.Feed(msg, ui)
		return next, deltas, text, isErr, done
	}
	next, deltas, _, isErr, done := feed(readResult("old\n", false))
	if done || isErr || len(deltas) != 0 {
		t.Fatalf("after a: %v %v %v", next, isErr, deltas)
	}
	reads = append(reads, next.GetReadArgs().GetPath())
	next, _, _, _, _ = feed(readResult("bye\n", false))
	reads = append(reads, next.GetReadArgs().GetPath())
	if strings.Join(reads, ",") != "a.go,c.go,d.go" {
		t.Fatalf("reads %v", reads)
	}
	next, deltas, _, _, _ = feed(readResult("d\n", false))
	if w := next.GetWriteArgs(); w.GetPath() != "a.go" || w.GetFileText() != "new\n" || w.GetToolCallId() != "p1" || len(deltas) != 0 {
		t.Fatalf("write a: %v %v", next, deltas)
	}

	type want struct {
		reply   *cursorpb.ExecClientMessage
		kind    string
		path    string
		cardID  string
		content string
	}
	steps := []want{
		{writeOK("a.go"), "write", "b.go", "p1_2", "package b\n"},
		{writeOK("b.go"), "delete", "c.go", "p1_3", ""},
		{deleteOK("c.go"), "write", "e.go", "p1_4", "e\n"},
		{writeOK("e.go"), "delete", "d.go", "p1_5", ""},
	}
	for i, s := range steps {
		next, deltas, _, isErr, done = feed(s.reply)
		if done || isErr || next == nil {
			t.Fatalf("step %d: %v %v", i, next, isErr)
		}
		// 上一步若不是第一张卡片，先收到它的完成；再收到这一步的开始。
		started := deltas[len(deltas)-1].GetInteractionUpdate().GetToolCallStarted()
		if started.GetCallId() != s.cardID {
			t.Fatalf("step %d started %v", i, deltas)
		}
		switch s.kind {
		case "write":
			if w := next.GetWriteArgs(); w.GetPath() != s.path || w.GetFileText() != s.content || w.GetToolCallId() != s.cardID {
				t.Fatalf("step %d: %v", i, next)
			}
		case "delete":
			if d := next.GetDeleteArgs(); d.GetPath() != s.path || d.GetToolCallId() != s.cardID || started.GetToolCall().GetDeleteToolCall() == nil {
				t.Fatalf("step %d: %v", i, next)
			}
		}
	}
	next, deltas, text, isErr, done := feed(deleteOK("d.go"))
	if next != nil || !done || isErr {
		t.Fatalf("final %v %v %q", next, isErr, text)
	}
	if text != "Success. Updated the following files:\nM a.go\nA b.go\nD c.go\nM d.go -> e.go" {
		t.Fatalf("summary %q", text)
	}
	if len(deltas) != 1 || deltas[0].GetInteractionUpdate().GetToolCallCompleted().GetCallId() != "p1_5" {
		t.Fatalf("last card completion %v", deltas)
	}
	if ui.GetEditToolCall().GetResult().GetSuccess().GetAfterFullFileContent() != "new\n" {
		t.Fatalf("first card result %v", ui.GetEditToolCall().GetResult())
	}
}

// 第二个文件对不上时，一个 Write 都不发，模型拿到整份没改的说明。
func TestMultiFilePatchMissChangesNothing(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a\n@@\n-x\n+X\n*** Update File: b\n@@\n-nothing like this\n+Y\n*** End Patch"
	_, ui, pending, err := Request(1, provider.ToolCall{ID: "p2", Name: "apply_patch", Arguments: patchArgs(t, patch)})
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, _, _ := pending.Feed(readResult("x\n", false), ui)
	if next.GetReadArgs().GetPath() != "b" {
		t.Fatalf("%v", next)
	}
	next, _, text, isErr, done := pending.Feed(readResult("y\n", false), ui)
	if next != nil || !done || !isErr || !strings.Contains(text, "b: apply_patch hunk 1") || !strings.Contains(text, "no files were changed") {
		t.Fatalf("%v %q", next, text)
	}
	if ui.GetEditToolCall().GetResult().GetError() == nil {
		t.Fatalf("first card must show the failure: %v", ui)
	}
}

// 写入中途被拒绝：停在那一步，说明哪些已改、哪些没改。
func TestMultiFilePatchStopsAtRejectedWrite(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a\n+1\n*** Add File: b\n+2\n*** Delete File: c\n*** End Patch"
	exec, ui, pending, err := Request(1, provider.ToolCall{ID: "p3", Name: "apply_patch", Arguments: patchArgs(t, patch)})
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetReadArgs().GetPath() != "c" {
		t.Fatalf("only the deleted file needs a read: %v", exec)
	}
	next, _, _, _, _ := pending.Feed(readResult("3\n", false), ui)
	if next.GetWriteArgs().GetPath() != "a" {
		t.Fatalf("%v", next)
	}
	next, _, _, _, _ = pending.Feed(writeOK("a"), ui)
	if next.GetWriteArgs().GetPath() != "b" {
		t.Fatalf("%v", next)
	}
	rejected := &cursorpb.ExecClientMessage{Message: &cursorpb.ExecClientMessage_WriteResult{WriteResult: &cursorpb.WriteResult{
		Result: &cursorpb.WriteResult_Rejected{Rejected: &cursorpb.WriteRejected{Path: "b", Reason: "user said no"}},
	}}}
	next, deltas, text, isErr, done := pending.Feed(rejected, ui)
	want := "apply_patch stopped at b: user said no\nAlready applied:\nA a\nNot applied:\nA b\nD c"
	if next != nil || !done || !isErr || text != want {
		t.Fatalf("%v %q", next, text)
	}
	if len(deltas) != 1 || deltas[0].GetInteractionUpdate().GetToolCallCompleted().GetToolCall().GetEditToolCall().GetResult().GetRejected() == nil {
		t.Fatalf("the rejected card is completed with the rejection: %v", deltas)
	}
}

// 只有新建文件时不用读；删除的文件不存在时整份不改。Shell 里的 heredoc 也走同一流程。
func TestMultiFilePatchAddOnlyAndMissingDelete(t *testing.T) {
	exec, _, _, err := Request(1, provider.ToolCall{ID: "p4", Name: "apply_patch", Arguments: patchArgs(t, "*** Begin Patch\n*** Add File: a\n+1\n*** Add File: b\n+2\n*** End Patch")})
	if err != nil || exec.GetWriteArgs().GetPath() != "a" || exec.GetWriteArgs().GetToolCallId() != "p4" || exec.GetId() != 1 {
		t.Fatalf("%v %v", exec, err)
	}
	cmd, _ := json.Marshal(map[string]string{"command": "apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: a\n@@\n-1\n+one\n*** Delete File: gone\n*** End Patch\nEOF"})
	exec, ui, pending, err := Request(2, provider.ToolCall{ID: "p5", Name: "Shell", Arguments: string(cmd)})
	if err != nil || exec.GetReadArgs().GetPath() != "a" {
		t.Fatalf("%v %v", exec, err)
	}
	pending.Feed(readResult("1\n", false), ui)
	next, _, text, isErr, _ := pending.Feed(notFound("gone"), ui)
	if next != nil || !isErr || text != "apply_patch: cannot delete gone: file not found; no files were changed" {
		t.Fatalf("%v %q", next, text)
	}
}

func TestPatchPathsListsEditedFiles(t *testing.T) {
	got := PatchPaths(provider.ToolCall{Name: "apply_patch", Arguments: patchArgs(t, flowPatch)})
	if strings.Join(got, ",") != "a.go,b.go,e.go" {
		t.Fatalf("%v", got)
	}
	if PatchPaths(provider.ToolCall{Name: "Shell", Arguments: `{"command":"ls"}`}) != nil {
		t.Fatal("plain shell is not an edit")
	}
}

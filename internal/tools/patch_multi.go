package tools

import (
	"fmt"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// 多文件 / Move / 混合操作的 apply_patch。
//
// 流程对齐 Codex（openai/codex codex-rs/apply-patch）的语义：按补丁里的顺序 Add（已存在则覆盖）、
// Update、Delete，Move 写新路径后删旧路径，结果是「Success. Updated the following files:」加 A/M/D 行。
// 与 Codex 不同的是先把所有要读的文件读完、在内存里算好全部结果再动手：
// 任何一块对不上、要改/删的文件不存在，整份补丁一个文件都不写，模型重发即可，不会留下改了一半的工作区。
// 写入阶段 Cursor 报错或用户拒绝时停在那一步，结果里写明已经改了哪些、哪些没改。

const (
	stepWrite = iota
	stepDelete
)

type patchFile struct {
	exists bool
	text   string // 已换成 LF
	binary bool   // 读到的是二进制或被截断，只能删，不能改
}

type patchStep struct {
	kind   int
	path   string
	before string
	after  string
	fresh  bool   // 写入目标原本不存在（Add、Move 的新路径）
	line   string // 这一步完成后在摘要里记的一行；Move 的写入步骤为空，删旧文件那步才记
	ui     *cursorpb.ToolCall
}

type patchState struct {
	callID string
	ops    []patchOp
	reads  []string
	files  map[string]patchFile
	steps  []patchStep
	next   int
	write  bool
	done   []string
	deltas []*cursorpb.AgentServerMessage
}

// patchReads 列出执行前要读的文件：要改、要删、要移走的文件，补丁前面已新建过的不必再读。
func patchReads(ops []patchOp) []string {
	known := map[string]bool{}
	var reads []string
	for _, op := range ops {
		if op.kind != "add" && !known[op.path] {
			reads = append(reads, op.path)
		}
		known[op.path] = true
		if op.move != "" {
			known[op.move] = true
		}
	}
	return reads
}

// patchSkeleton 按操作顺序给出每一步的种类和路径（内容稍后由 planPatch 填上）。卡片按步骤一一对应。
func patchSkeleton(ops []patchOp) []patchStep {
	var steps []patchStep
	for _, op := range ops {
		switch {
		case op.kind == "delete":
			steps = append(steps, patchStep{kind: stepDelete, path: op.path, line: "D " + op.path})
		case op.kind == "add":
			steps = append(steps, patchStep{kind: stepWrite, path: op.path, line: "A " + op.path})
		case op.move != "":
			steps = append(steps,
				patchStep{kind: stepWrite, path: op.move},
				patchStep{kind: stepDelete, path: op.path, line: "M " + op.path + " -> " + op.move})
		default:
			steps = append(steps, patchStep{kind: stepWrite, path: op.path, line: "M " + op.path})
		}
	}
	return steps
}

// planPatch 在内存里按顺序执行补丁，填好每一步的前后内容。出错时什么都不该写。
func planPatch(ops []patchOp, files map[string]patchFile) ([]patchStep, error) {
	vfs := map[string]patchFile{}
	for k, v := range files {
		vfs[k] = v
	}
	steps := patchSkeleton(ops)
	k := 0
	for _, op := range ops {
		f := vfs[op.path]
		switch {
		case op.kind == "add":
			content := strings.Join(op.add, "\n") + "\n"
			steps[k].before, steps[k].fresh = f.text, !f.exists
			steps[k].after = restoreNewlines(content, f.exists && usesCRLF(f.text))
			vfs[op.path] = patchFile{exists: true, text: steps[k].after}
			k++
		case op.kind == "delete":
			if !f.exists {
				return nil, fmt.Errorf("cannot delete %s: file not found", op.path)
			}
			steps[k].before = f.text
			vfs[op.path] = patchFile{}
			k++
		default:
			if !f.exists {
				return nil, fmt.Errorf("cannot update %s: file not found", op.path)
			}
			if f.binary {
				return nil, fmt.Errorf("cannot update %s: it is binary or too large to read in full", op.path)
			}
			after := f.text
			if len(op.hunks) > 0 {
				var err error
				if after, err = applyPatchHunks(f.text, op.hunks); err != nil {
					return nil, fmt.Errorf("%s: %v", op.path, err)
				}
			}
			if op.move == "" {
				steps[k].before, steps[k].after = f.text, after
				vfs[op.path] = patchFile{exists: true, text: after}
				k++
				continue
			}
			if op.move == op.path {
				return nil, fmt.Errorf("%s: '*** Move to:' names the same file", op.path)
			}
			// 写入的是新路径：卡片的 before 是新路径原来的内容（通常不存在），不是旧文件。
			dst := vfs[op.move]
			steps[k].before, steps[k].after, steps[k].fresh = dst.text, after, !dst.exists
			steps[k+1].before = f.text
			vfs[op.move] = patchFile{exists: true, text: after}
			vfs[op.path] = patchFile{}
			k += 2
		}
	}
	return steps, nil
}

// ApplyPatchFiles 在内存里执行一份 apply_patch，供评测和测试用。files 是补丁前的文件（路径→内容，LF），
// 返回补丁后发生变化的文件（nil 表示被删）和交给模型的摘要。
func ApplyPatchFiles(text string, files map[string]string) (map[string]*string, string, error) {
	ops, err := parsePatch(text)
	if err != nil {
		return nil, "", err
	}
	known := map[string]patchFile{}
	for _, path := range patchReads(ops) {
		if content, ok := files[path]; ok {
			known[path] = patchFile{exists: true, text: normalizeNewlines(content)}
		}
	}
	steps, err := planPatch(ops, known)
	if err != nil {
		return nil, "", fmt.Errorf("apply_patch: %v; no files were changed", err)
	}
	out := map[string]*string{}
	var lines []string
	for _, step := range steps {
		if step.kind == stepDelete {
			out[step.path] = nil
		} else {
			after := step.after
			out[step.path] = &after
		}
		if step.line != "" {
			lines = append(lines, step.line)
		}
	}
	return out, patchSummary(lines), nil
}

func patchSummary(lines []string) string {
	return "Success. Updated the following files:\n" + strings.Join(lines, "\n")
}

// startPatch 开始一次多文件补丁：第一张卡片对应第一步，先读第一个要读的文件。
func startPatch(id uint32, call provider.ToolCall, ops []patchOp) (*cursorpb.ExecServerMessage, *cursorpb.ToolCall, *Pending, error) {
	st := &patchState{callID: call.ID, ops: ops, reads: patchReads(ops), files: map[string]patchFile{}}
	st.steps = patchSkeleton(ops)
	for i := range st.steps {
		cardID := call.ID
		if i > 0 {
			cardID = fmt.Sprintf("%s_%d", call.ID, i+1)
		}
		st.steps[i].ui = patchCard(cardID, st.steps[i], stepStream(ops, st.steps[i]))
	}
	ui := st.steps[0].ui
	p := &Pending{Call: call, patch: st}
	exec := &cursorpb.ExecServerMessage{Id: id, ExecId: fmt.Sprintf("%s-%d", call.ID, id)}
	if len(st.reads) > 0 {
		exec.Message = st.readArgs(st.reads[0])
		return exec, ui, p, nil
	}
	// 只有 Add：不用读，直接算好开始写。
	next, text, _ := st.begin()
	if next == nil {
		return nil, nil, nil, fmt.Errorf("%s", text)
	}
	exec.Message = next.Message
	return exec, ui, p, nil
}

// stepStream 是卡片里流式展示的内容：写入步骤给出这段补丁原文（Add 给出文件内容）。
func stepStream(ops []patchOp, step patchStep) string {
	if step.kind != stepWrite {
		return ""
	}
	for _, op := range ops {
		if op.path == step.path && op.kind == "add" {
			return strings.Join(op.add, "\n") + "\n"
		}
		if op.path == step.path || op.move == step.path {
			return strings.Join(op.raw, "\n")
		}
	}
	return ""
}

func patchCard(id string, step patchStep, stream string) *cursorpb.ToolCall {
	ui := &cursorpb.ToolCall{ToolCallId: &id}
	if step.kind == stepDelete {
		ui.Tool = &cursorpb.ToolCall_DeleteToolCall{DeleteToolCall: &cursorpb.DeleteToolCall{Args: &cursorpb.DeleteArgs{Path: step.path, ToolCallId: id}}}
		return ui
	}
	ui.Tool = &cursorpb.ToolCall_EditToolCall{EditToolCall: &cursorpb.EditToolCall{Args: &cursorpb.EditArgs{Path: step.path, StreamContent: &stream}}}
	return ui
}

func (st *patchState) readArgs(path string) *cursorpb.ExecServerMessage_ReadArgs {
	return &cursorpb.ExecServerMessage_ReadArgs{ReadArgs: &cursorpb.ReadArgs{Path: path, ToolCallId: st.callID}}
}

func (st *patchState) stepExec(step patchStep) *cursorpb.ExecServerMessage {
	id := step.ui.GetToolCallId()
	if step.kind == stepDelete {
		return &cursorpb.ExecServerMessage{Message: &cursorpb.ExecServerMessage_DeleteArgs{DeleteArgs: &cursorpb.DeleteArgs{Path: step.path, ToolCallId: id}}}
	}
	return &cursorpb.ExecServerMessage{Message: &cursorpb.ExecServerMessage_WriteArgs{WriteArgs: &cursorpb.WriteArgs{
		Path: step.path, FileText: step.after, ToolCallId: id,
	}}}
}

// advance 消化一条执行结果：读阶段收集文件，读完后算好全部结果；写阶段逐步下发并记录摘要。
func (st *patchState) advance(msg *cursorpb.ExecClientMessage) (*cursorpb.ExecServerMessage, string, bool) {
	if !st.write {
		path := st.reads[st.next]
		read := msg.GetReadResult()
		if read == nil {
			return st.fail(fmt.Sprintf("apply_patch: expected a read result for %s; no files were changed", path))
		}
		f, err := patchFileFromRead(read)
		if err != nil {
			return st.fail(fmt.Sprintf("apply_patch: cannot read %s: %v; no files were changed", path, err))
		}
		st.files[path] = f
		st.next++
		if st.next < len(st.reads) {
			return &cursorpb.ExecServerMessage{Message: st.readArgs(st.reads[st.next])}, "", false
		}
		return st.begin()
	}
	step := &st.steps[st.next]
	var text string
	var isErr bool
	switch step.kind {
	case stepDelete:
		r := msg.GetDeleteResult()
		if r == nil {
			text, isErr = "Cursor returned an unexpected result for a delete", true
			break
		}
		text, isErr = deleteText(r)
		if tool, ok := step.ui.GetTool().(*cursorpb.ToolCall_DeleteToolCall); ok {
			tool.DeleteToolCall.Result = r
		}
	default:
		r := msg.GetWriteResult()
		if r == nil {
			text, isErr = "Cursor returned an unexpected result for a write", true
			failEdit(step.ui, step.path, text)
			break
		}
		text, isErr = writeText(r)
		if tool, ok := step.ui.GetTool().(*cursorpb.ToolCall_EditToolCall); ok {
			tool.EditToolCall.Result = editResultFromWrite(&Pending{edit: &editState{path: step.path, before: step.before, after: step.after, fresh: step.fresh}}, r)
		}
	}
	if st.next > 0 {
		st.deltas = append(st.deltas, cardUpdate(false, step.ui))
	}
	if isErr {
		return nil, st.stopped(step.path, text), true
	}
	if step.line != "" {
		st.done = append(st.done, step.line)
	}
	st.next++
	if st.next < len(st.steps) {
		st.deltas = append(st.deltas, cardUpdate(true, st.steps[st.next].ui))
		return st.stepExec(st.steps[st.next]), "", false
	}
	return nil, patchSummary(st.done), false
}

// begin 读完之后算好全部结果并下发第一步。
func (st *patchState) begin() (*cursorpb.ExecServerMessage, string, bool) {
	planned, err := planPatch(st.ops, st.files)
	if err != nil {
		return st.fail(fmt.Sprintf("apply_patch: %v; no files were changed", err))
	}
	for i := range planned {
		planned[i].ui = st.steps[i].ui
	}
	st.steps = planned
	st.write = true
	st.next = 0
	return st.stepExec(st.steps[0]), "", false
}

func (st *patchState) fail(text string) (*cursorpb.ExecServerMessage, string, bool) {
	first := st.steps[0]
	if first.kind == stepDelete {
		if tool, ok := first.ui.GetTool().(*cursorpb.ToolCall_DeleteToolCall); ok {
			tool.DeleteToolCall.Result = &cursorpb.DeleteResult{Result: &cursorpb.DeleteResult_Error{Error: &cursorpb.DeleteError{Path: first.path, Error: text}}}
		}
	} else {
		failEdit(first.ui, first.path, text)
	}
	return nil, text, true
}

// stopped 是写入阶段中途失败时交给模型的说明：哪一步失败、哪些已经改了、哪些没动。
func (st *patchState) stopped(path, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apply_patch stopped at %s: %s", path, reason)
	if len(st.done) > 0 {
		b.WriteString("\nAlready applied:\n" + strings.Join(st.done, "\n"))
	} else {
		b.WriteString("\nNo files were changed before this step.")
	}
	var left []string
	for _, s := range st.steps[st.next:] {
		if s.line != "" {
			left = append(left, s.line)
		}
	}
	if len(left) > 0 {
		b.WriteString("\nNot applied:\n" + strings.Join(left, "\n"))
	}
	return b.String()
}

func (st *patchState) takeDeltas() []*cursorpb.AgentServerMessage {
	d := st.deltas
	st.deltas = nil
	return d
}

func patchFileFromRead(r *cursorpb.ReadResult) (patchFile, error) {
	switch v := r.GetResult().(type) {
	case *cursorpb.ReadResult_Success:
		if v.Success.GetTruncated() {
			return patchFile{exists: true, binary: true}, nil
		}
		switch out := v.Success.GetOutput().(type) {
		case *cursorpb.ReadSuccess_Content:
			return patchFile{exists: true, text: normalizeNewlines(out.Content)}, nil
		case *cursorpb.ReadSuccess_Data:
			return patchFile{exists: true, binary: true}, nil
		}
		return patchFile{exists: true}, nil
	case *cursorpb.ReadResult_FileNotFound:
		return patchFile{}, nil
	case *cursorpb.ReadResult_Error:
		return patchFile{}, fmt.Errorf("%s", v.Error.GetError())
	case *cursorpb.ReadResult_Rejected:
		return patchFile{}, fmt.Errorf("the user rejected reading it: %s", v.Rejected.GetReason())
	case *cursorpb.ReadResult_PermissionDenied:
		return patchFile{}, fmt.Errorf("read permission denied")
	case *cursorpb.ReadResult_InvalidFile:
		return patchFile{}, fmt.Errorf("%s", v.InvalidFile.GetReason())
	}
	return patchFile{}, fmt.Errorf("the read result is empty")
}

func cardUpdate(started bool, ui *cursorpb.ToolCall) *cursorpb.AgentServerMessage {
	id := ui.GetToolCallId()
	u := &cursorpb.InteractionUpdate{}
	if started {
		u.Message = &cursorpb.InteractionUpdate_ToolCallStarted{ToolCallStarted: &cursorpb.ToolCallStartedUpdate{CallId: id, ToolCall: ui}}
	} else {
		u.Message = &cursorpb.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &cursorpb.ToolCallCompletedUpdate{CallId: id, ToolCall: ui}}
	}
	return &cursorpb.AgentServerMessage{Message: &cursorpb.AgentServerMessage_InteractionUpdate{InteractionUpdate: u}}
}

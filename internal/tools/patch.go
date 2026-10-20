package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor-inner/internal/provider"
)

// Codex / GPT 系模型习惯输出的 apply_patch 格式：
//
//	*** Begin Patch
//	*** Update File: path
//	@@ 可选的定位行
//	 上下文
//	-删掉的行
//	+新加的行
//	*** End Patch
//
// 单个 Update 走与 StrReplace 相同的读改写流程（逐块匹配，容忍空白/行号/弯引号，最后才模糊匹配），
// 单个 Add 等同 Write，单个 Delete 等同 Delete。多文件、Move，以及混合操作走 patchState：
// 先经 Cursor 读完所有要改/删的文件，在内存里按顺序算出每个文件的结果，任何一块对不上就整份不写；
// 全部算好之后再逐个下发 Write / Delete，每个文件一张卡片，结果按 Codex 的
// “Success. Updated the following files:”格式交回模型。

type patchOp struct {
	kind  string // update / add / delete
	path  string
	move  string
	hunks []patchHunk
	add   []string
	raw   []string // 这一段补丁的原文（含头行），给界面卡片做流式内容
}

type patchHunk struct {
	anchor   string
	old, new []string
}

const (
	patchBegin = "*** Begin Patch"
	patchEnd   = "*** End Patch"
)

// parsePatch 解析 apply_patch 文本。缺 Begin/End 也接受；上下文行漏了前导空格时按上下文处理。
func parsePatch(text string) ([]patchOp, error) {
	text = normalizeNewlines(text)
	if i := strings.Index(text, patchBegin); i >= 0 {
		text = text[i+len(patchBegin):]
	}
	if i := strings.Index(text, patchEnd); i >= 0 {
		text = text[:i]
	}
	var ops []patchOp
	var cur *patchOp
	var hunk *patchHunk
	flush := func() {
		if cur != nil && hunk != nil && (len(hunk.old) > 0 || len(hunk.new) > 0) {
			cur.hunks = append(cur.hunks, *hunk)
		}
		hunk = nil
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File:"), strings.HasPrefix(line, "*** Add File:"), strings.HasPrefix(line, "*** Delete File:"):
			flush()
			kind := "update"
			if strings.HasPrefix(line, "*** Add File:") {
				kind = "add"
			} else if strings.HasPrefix(line, "*** Delete File:") {
				kind = "delete"
			}
			ops = append(ops, patchOp{kind: kind, path: strings.TrimSpace(line[strings.Index(line, ":")+1:]), raw: []string{line}})
			cur = &ops[len(ops)-1]
			continue
		case strings.HasPrefix(line, "*** Move to:"):
			if cur != nil {
				cur.move = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to:"))
				cur.raw = append(cur.raw, line)
			}
			continue
		case strings.HasPrefix(line, "*** End of File"):
			continue
		}
		if cur != nil {
			cur.raw = append(cur.raw, line)
		}
		if cur == nil {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, fmt.Errorf("apply_patch: expected '*** Update File:' or '*** Add File:' before %q", line)
		}
		if cur.kind == "add" {
			cur.add = append(cur.add, strings.TrimPrefix(line, "+"))
			continue
		}
		if cur.kind != "update" {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			flush()
			hunk = &patchHunk{anchor: strings.TrimSpace(strings.TrimPrefix(line, "@@"))}
			continue
		}
		if hunk == nil {
			hunk = &patchHunk{}
		}
		switch {
		case strings.HasPrefix(line, "-"):
			hunk.old = append(hunk.old, line[1:])
		case strings.HasPrefix(line, "+"):
			hunk.new = append(hunk.new, line[1:])
		case strings.HasPrefix(line, " "):
			hunk.old = append(hunk.old, line[1:])
			hunk.new = append(hunk.new, line[1:])
		default:
			hunk.old = append(hunk.old, line)
			hunk.new = append(hunk.new, line)
		}
	}
	flush()
	for i := range ops {
		kept := ops[i].hunks[:0]
		for _, h := range ops[i].hunks {
			h.old, h.new = trimBlankEdges(h.old, h.new)
			if len(h.old) > 0 || len(h.new) > 0 {
				kept = append(kept, h)
			}
		}
		ops[i].hunks = kept
		if ops[i].kind == "add" {
			for len(ops[i].add) > 0 && ops[i].add[len(ops[i].add)-1] == "" {
				ops[i].add = ops[i].add[:len(ops[i].add)-1]
			}
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("apply_patch: no file operations found")
	}
	for _, op := range ops {
		if op.path == "" {
			return nil, fmt.Errorf("apply_patch: a file header has no path")
		}
		if op.kind == "update" && len(op.hunks) == 0 && op.move == "" {
			return nil, fmt.Errorf("apply_patch: %s has no hunks", op.path)
		}
	}
	return ops, nil
}

// trimBlankEdges 去掉两边共同的首尾空上下文行（多是模型多打的空行）。
func trimBlankEdges(old, new []string) ([]string, []string) {
	for len(old) > 0 && len(new) > 0 && old[len(old)-1] == "" && new[len(new)-1] == "" {
		old, new = old[:len(old)-1], new[:len(new)-1]
	}
	for len(old) > 0 && len(new) > 0 && old[0] == "" && new[0] == "" {
		old, new = old[1:], new[1:]
	}
	return old, new
}

// applyPatchHunks 依次把每块改到 before 上。纯新增块（没有上下文）按定位行插在其后，没有定位行就追加到末尾。
func applyPatchHunks(before string, hunks []patchHunk) (string, error) {
	crlf := usesCRLF(before)
	cur := normalizeNewlines(before)
	for i, h := range hunks {
		next := strings.Join(h.new, "\n")
		if len(h.old) == 0 {
			if h.anchor != "" {
				if idx := strings.Index(cur, h.anchor); idx >= 0 {
					end := strings.Index(cur[idx:], "\n")
					if end < 0 {
						cur += "\n" + next
					} else {
						at := idx + end + 1
						cur = cur[:at] + next + "\n" + cur[at:]
					}
					continue
				}
			}
			if cur != "" && !strings.HasSuffix(cur, "\n") {
				cur += "\n"
			}
			cur += next + "\n"
			continue
		}
		after, err := replaceNormalized(cur, strings.Join(h.old, "\n"), next, false)
		if err != nil && h.anchor != "" && strings.Contains(err.Error(), "not unique") {
			// 与 Codex 一致：@@ 定位行用来缩小范围。整文件里不唯一时，只在定位行之后再找一次。
			if at := anchorLine(cur, h.anchor); at >= 0 {
				if tail, terr := replaceNormalized(cur[at:], strings.Join(h.old, "\n"), next, false); terr == nil {
					after, err = cur[:at]+tail, nil
				}
			}
		}
		if err != nil {
			return "", fmt.Errorf("apply_patch hunk %d: %v; re-read the file and resend that hunk with exact context lines", i+1, err)
		}
		cur = after
	}
	return restoreNewlines(cur, crlf), nil
}

// anchorLine 返回唯一一行包含定位文本的行首位置；找不到或不唯一时返回 -1。
func anchorLine(text, anchor string) int {
	at := -1
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.Contains(line, anchor) {
			if at >= 0 {
				return -1
			}
			at = offset
		}
		offset += len(line)
	}
	return at
}

// patchCall 把 apply_patch（工具调用或 Shell 里的 heredoc）改写成 cursor-inner 已有的单文件工具。
// 返回的 ops 非空时，这份补丁要走多文件流程（startPatch），改写后的调用无意义。
func patchCall(call provider.ToolCall, text string) (provider.ToolCall, []patchOp, error) {
	ops, err := parsePatch(text)
	if err != nil {
		return call, nil, err
	}
	if len(ops) != 1 || ops[0].move != "" {
		return call, ops, nil
	}
	op := ops[0]
	switch op.kind {
	case "delete":
		raw, _ := json.Marshal(map[string]string{"path": op.path})
		call.Name, call.Arguments = "Delete", string(raw)
		return call, nil, nil
	case "add":
		raw, _ := json.Marshal(map[string]string{"path": op.path, "contents": strings.Join(op.add, "\n") + "\n"})
		call.Name, call.Arguments = "Write", string(raw)
		return call, nil, nil
	}
	raw, _ := json.Marshal(map[string]string{"path": op.path, "patch": text})
	call.Name, call.Arguments = "ApplyPatch", string(raw)
	return call, nil, nil
}

// PatchPaths 列出一次 apply_patch（工具调用或 Shell heredoc）新建或改过的文件，不含删掉的。
// 补丁解析不了时返回空。
func PatchPaths(call provider.ToolCall) []string {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return nil
	}
	var text string
	switch normalize(call.Name) {
	case "shell", "bash":
		command, _ := a.str("command")
		text, _ = shellPatch(command)
	case "applypatch":
		text, _ = a.rawString("patch", "input", "diff")
	}
	ops, err := parsePatch(text)
	if err != nil {
		return nil
	}
	var out []string
	for _, op := range ops {
		switch {
		case op.kind == "delete":
		case op.move != "":
			out = append(out, op.move)
		default:
			out = append(out, op.path)
		}
	}
	return out
}

// shellPatch 认出 Shell 里的 `apply_patch <<'EOF' … EOF`（Codex 的习惯写法），返回补丁正文。
func shellPatch(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	head := trimmed
	if i := strings.LastIndex(head, "&&"); i >= 0 && i < strings.Index(head+"\n", "\n") {
		head = strings.TrimSpace(head[i+2:])
	}
	if !strings.HasPrefix(head, "apply_patch") && !strings.HasPrefix(head, "applypatch") {
		return "", false
	}
	start := strings.Index(trimmed, patchBegin)
	end := strings.LastIndex(trimmed, patchEnd)
	if start < 0 || end < start {
		return "", false
	}
	return trimmed[start : end+len(patchEnd)], true
}

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
// 这里一次只处理一个文件：Update 走与 StrReplace 相同的读改写流程（逐块匹配，容忍空白/行号/弯引号，
// 最后才模糊匹配），Add 等同 Write。多文件、Delete、Move 会请模型拆开或改用对应工具。

type patchOp struct {
	kind  string // update / add / delete
	path  string
	move  string
	hunks []patchHunk
	add   []string
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
			ops = append(ops, patchOp{kind: kind, path: strings.TrimSpace(line[strings.Index(line, ":")+1:])})
			cur = &ops[len(ops)-1]
			continue
		case strings.HasPrefix(line, "*** Move to:"):
			if cur != nil {
				cur.move = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to:"))
			}
			continue
		case strings.HasPrefix(line, "*** End of File"):
			continue
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
		for j := range ops[i].hunks {
			h := &ops[i].hunks[j]
			h.old, h.new = trimBlankEdges(h.old, h.new)
		}
		if ops[i].kind == "add" {
			for len(ops[i].add) > 0 && ops[i].add[len(ops[i].add)-1] == "" {
				ops[i].add = ops[i].add[:len(ops[i].add)-1]
			}
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("apply_patch: no file operations found")
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
		if err != nil {
			return "", fmt.Errorf("apply_patch hunk %d: %v; re-read the file and resend that hunk with exact context lines", i+1, err)
		}
		cur = after
	}
	return restoreNewlines(cur, crlf), nil
}

// patchCall 把 apply_patch（工具调用或 Shell 里的 heredoc）改写成 cursor-inner 已有的单文件编辑。
func patchCall(call provider.ToolCall, text string) (provider.ToolCall, error) {
	ops, err := parsePatch(text)
	if err != nil {
		return call, err
	}
	if len(ops) != 1 {
		return call, fmt.Errorf("apply_patch here changes one file per call; this patch touches %d files, send one apply_patch per file", len(ops))
	}
	op := ops[0]
	if op.move != "" {
		return call, fmt.Errorf("apply_patch: '*** Move to:' is not supported; write the new file and delete the old one instead")
	}
	switch op.kind {
	case "delete":
		return call, fmt.Errorf("apply_patch: use the Delete tool to delete %s", op.path)
	case "add":
		raw, _ := json.Marshal(map[string]string{"path": op.path, "contents": strings.Join(op.add, "\n") + "\n"})
		call.Name, call.Arguments = "Write", string(raw)
		return call, nil
	}
	if len(op.hunks) == 0 {
		return call, fmt.Errorf("apply_patch: %s has no hunks", op.path)
	}
	raw, _ := json.Marshal(map[string]string{"path": op.path, "patch": text})
	call.Name, call.Arguments = "ApplyPatch", string(raw)
	return call, nil
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

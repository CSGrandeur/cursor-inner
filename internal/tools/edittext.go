package tools

import (
	"fmt"

	"cursor-inner/internal/provider"
)

// EditText 在内存里执行一次单文件编辑类调用（StrReplace / Write / ApplyPatch / Shell 里的 apply_patch），
// 与真实读改写流程用同一套匹配逻辑。供评测和测试用，不碰磁盘。多文件补丁用 ApplyPatchFiles。
func EditText(call provider.ToolCall, before string) (string, error) {
	a, err := parseArgs(call.Arguments)
	if err != nil {
		return "", err
	}
	switch normalize(call.Name) {
	case "shell", "bash":
		command, _ := a.str("command")
		text, ok := shellPatch(command)
		if !ok {
			return "", fmt.Errorf("not an apply_patch command")
		}
		return editPatchText(call, text, before)
	case "applypatch":
		if _, ok := a.str("path"); !ok {
			text, _ := a.rawString("patch", "input", "diff")
			return editPatchText(call, text, before)
		}
		text, _ := a.rawString("patch")
		ops, err := parsePatch(text)
		if err != nil {
			return "", err
		}
		return applyPatchHunks(before, ops[0].hunks)
	case "write":
		contents, _ := a.rawString("contents", "content")
		return restoreNewlines(normalizeNewlines(contents), usesCRLF(before)), nil
	case "strreplace":
		return replaceString(a, before)
	}
	return "", fmt.Errorf("%s is not an edit tool", call.Name)
}

func editPatchText(call provider.ToolCall, text, before string) (string, error) {
	rewritten, ops, err := patchCall(call, text)
	if err != nil {
		return "", err
	}
	if ops != nil {
		return "", fmt.Errorf("apply_patch touches %d files; use ApplyPatchFiles", len(ops))
	}
	if normalize(rewritten.Name) == "delete" {
		return "", nil
	}
	return EditText(rewritten, before)
}

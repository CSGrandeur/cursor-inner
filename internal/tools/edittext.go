package tools

import (
	"fmt"

	"cursor-inner/internal/provider"
)

// EditText 在内存里执行一次编辑类调用（StrReplace / Write / ApplyPatch / Shell 里的 apply_patch），
// 与真实读改写流程用同一套匹配逻辑。供评测和测试用，不碰磁盘。
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
		rewritten, err := patchCall(call, text)
		if err != nil {
			return "", err
		}
		return EditText(rewritten, before)
	case "applypatch":
		if _, ok := a.str("path"); !ok {
			text, _ := a.rawString("patch", "input", "diff")
			rewritten, err := patchCall(call, text)
			if err != nil {
				return "", err
			}
			return EditText(rewritten, before)
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

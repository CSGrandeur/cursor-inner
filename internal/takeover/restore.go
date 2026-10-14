package takeover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"cursor-inner/internal/fsutil"
)

// restore.json 记下 cursor-inner 退出时关掉的 Grok / Codex，下次启动时提供一键恢复。

func restorePath(dir string) string { return filepath.Join(dir, "restore.json") }

// PendingRestore 返回上次退出时关掉、还没恢复的程序（"grok"、"codex"）。
func PendingRestore(dir string) []string {
	raw, err := os.ReadFile(restorePath(dir))
	if err != nil {
		return nil
	}
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		return nil
	}
	var out []string
	for _, n := range []string{"grok", "codex"} {
		if slices.Contains(names, n) {
			out = append(out, n)
		}
	}
	return out
}

// NoteRestore 把 names 加进待恢复列表。
func NoteRestore(dir string, names ...string) {
	if len(names) == 0 {
		return
	}
	cur := PendingRestore(dir)
	for _, n := range names {
		if !slices.Contains(cur, n) {
			cur = append(cur, n)
		}
	}
	writeRestore(dir, cur)
}

// ClearRestore 从待恢复列表去掉 names；不给 names 时清空。
func ClearRestore(dir string, names ...string) {
	if len(names) == 0 {
		_ = os.Remove(restorePath(dir))
		return
	}
	var left []string
	for _, n := range PendingRestore(dir) {
		if !slices.Contains(names, n) {
			left = append(left, n)
		}
	}
	writeRestore(dir, left)
}

func writeRestore(dir string, names []string) {
	if len(names) == 0 {
		_ = os.Remove(restorePath(dir))
		return
	}
	raw, _ := json.Marshal(names)
	_ = os.MkdirAll(dir, 0o755)
	_ = fsutil.WriteFile(restorePath(dir), raw)
}

package codexbot

import (
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func findCodex(candidates []string, lookups []func() (string, error)) (string, error) {
	for _, path := range candidates {
		if exe := codexExecutable(path); exe != "" {
			return exe, nil
		}
	}
	for _, lookup := range lookups {
		if lookup == nil {
			continue
		}
		path, err := lookup()
		if err != nil {
			continue
		}
		if exe := codexExecutable(path); exe != "" {
			return exe, nil
		}
	}
	return "", i18n.E("找不到 Codex。请先安装，或从开始菜单打开一次。", "Codex was not found. Install it, or open it once from the Start menu.")
}

// codexExecutable 只接受桌面壳的路径。resources 里的 CLI、bin 里的 codex.exe 不算。
func codexExecutable(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"`)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	path = os.ExpandEnv(path)
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		for _, name := range []string{"Codex.exe", "ChatGPT.exe"} {
			nested := filepath.Join(path, "app", name)
			if isDesktopShell(nested, "") && isFile(nested) {
				return nested
			}
			flat := filepath.Join(path, name)
			if isDesktopShell(flat, "") && isFile(flat) {
				return flat
			}
		}
		return ""
	}
	if isDesktopShell(path, "") && isFile(path) {
		return path
	}
	return ""
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

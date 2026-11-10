package antigravitylaunch

import (
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func findAntigravity(candidates []string, lookup func() (string, error)) (string, error) {
	for _, path := range candidates {
		if exe := antigravityExecutable(path); exe != "" {
			return exe, nil
		}
	}
	if lookup != nil {
		if path, err := lookup(); err == nil {
			if exe := antigravityExecutable(path); exe != "" {
				return exe, nil
			}
		}
	}
	return "", i18n.E("找不到 Antigravity（反重力）。请先安装，或从开始菜单打开一次。", "Antigravity was not found. Install it, or open it once from the Start menu.")
}

// antigravityExecutable 把安装目录里的 Antigravity.exe，或 resources/app/bin 下的命令，解析成桌面程序。
func antigravityExecutable(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "antigravity", "antigravity.cmd", "antigravity.bat":
		exe := filepath.Clean(filepath.Join(filepath.Dir(path), "..", "..", "..", "Antigravity.exe"))
		if isFile(exe) {
			return exe
		}
	case "antigravity.exe":
		if isFile(path) {
			return path
		}
	}
	return ""
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

package cursorlaunch

import (
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func findCursor(candidates []string, lookup func() (string, error)) (string, error) {
	for _, path := range candidates {
		if exe := cursorExecutable(path); exe != "" {
			return exe, nil
		}
	}
	if lookup != nil {
		if path, err := lookup(); err == nil {
			if exe := cursorExecutable(path); exe != "" {
				return exe, nil
			}
		}
	}
	return "", i18n.E("找不到 Cursor。请先安装，或从开始菜单打开一次。", "Cursor was not found. Install it, or open it once from the Start menu.")
}

// cursorExecutable 把安装目录里的 Cursor.exe，或 resources/app/bin 下的 cursor 命令，解析成桌面程序。
// bin 里的 cursor.cmd 会以命令行方式启动，不会打开窗口。
func cursorExecutable(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "cursor", "cursor.cmd", "cursor.bat":
		exe := filepath.Clean(filepath.Join(filepath.Dir(path), "..", "..", "..", "Cursor.exe"))
		if isFile(exe) {
			return exe
		}
	case "cursor.exe":
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

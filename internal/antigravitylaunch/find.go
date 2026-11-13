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

// antigravityExecutable 把安装目录、卸载项图标或 resources/app/bin 下的命令，解析成 Antigravity.exe。
func antigravityExecutable(path string) string {
	path = cleanInstallPath(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		nested := filepath.Join(path, "Antigravity.exe")
		if isFile(nested) {
			return nested
		}
		return ""
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "antigravity", "antigravity.cmd", "antigravity.bat":
		exe := filepath.Clean(filepath.Join(filepath.Dir(path), "..", "..", "..", "Antigravity.exe"))
		if isFile(exe) {
			return exe
		}
	case "antigravity.exe":
		return path
	}
	return ""
}

func cleanInstallPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"`)
	if i := strings.LastIndex(path, ","); i > 1 {
		suffix := strings.TrimSpace(path[i+1:])
		if suffix == "0" || suffix == "-0" {
			path = strings.TrimSpace(path[:i])
			path = strings.Trim(path, `"`)
		}
	}
	return path
}

func exeFromUninstall(displayName, installLocation, displayIcon string) string {
	if !strings.Contains(strings.ToLower(displayName), "antigravity") {
		return ""
	}
	if exe := antigravityExecutable(displayIcon); exe != "" {
		return exe
	}
	return antigravityExecutable(installLocation)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

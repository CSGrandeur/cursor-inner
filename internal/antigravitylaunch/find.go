package antigravitylaunch

import (
	"os"
	"path/filepath"
	"runtime"
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ResourcesDirs 返回本机 Antigravity 的 resources 目录候选（按优先级去重，仅保留真实存在的目录）。
func ResourcesDirs(hintExe string) []string {
	var rawDirs []string
	addExe := func(exe string) {
		exe = cleanInstallPath(exe)
		if exe == "" {
			return
		}
		rawDirs = append(rawDirs, resourcesDirsForExe(exe)...)
		if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" && resolved != exe {
			rawDirs = append(rawDirs, resourcesDirsForExe(resolved)...)
		}
	}
	addExe(hintExe)
	if detected := installedExe(); detected != "" {
		addExe(detected)
	}
	rawDirs = append(rawDirs, standardResourcesDirs()...)
	return uniqueExistingDirs(rawDirs)
}

func resourcesDirsForExe(exe string) []string {
	exe = cleanInstallPath(exe)
	if exe == "" {
		return nil
	}
	dir := exe
	var out []string
	if isDir(exe) {
		if strings.HasSuffix(strings.ToLower(exe), ".app") {
			out = append(out, filepath.Join(exe, "Contents", "Resources"))
		}
	} else {
		dir = filepath.Dir(exe)
	}
	out = append(out,
		filepath.Join(dir, "resources"),
		filepath.Join(dir, "Resources"),
		filepath.Join(dir, "..", "Resources"),
		filepath.Join(dir, "..", "resources"),
		filepath.Join(dir, "..", "..", "Resources"),
		filepath.Join(dir, "..", "..", "resources"),
	)
	up2 := filepath.Clean(filepath.Join(dir, "..", ".."))
	if strings.EqualFold(filepath.Base(up2), "resources") {
		out = append(out, up2)
	}
	return out
}

func standardResourcesDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		dirs := []string{"/Applications/Antigravity.app/Contents/Resources"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Applications", "Antigravity.app", "Contents", "Resources"))
		}
		return dirs
	case "linux":
		dirs := []string{
			"/opt/Antigravity/resources",
			"/opt/antigravity/resources",
			"/usr/share/antigravity/resources",
			"/usr/lib/antigravity/resources",
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".local", "share", "antigravity", "resources"))
		}
		return dirs
	case "windows":
		var dirs []string
		local := os.Getenv("LOCALAPPDATA")
		if local == "" && home != "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		if local != "" {
			dirs = append(dirs,
				filepath.Join(local, "Programs", "antigravity", "resources"),
				filepath.Join(local, "Programs", "Antigravity", "resources"),
			)
		}
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if pf == "" {
				continue
			}
			dirs = append(dirs,
				filepath.Join(pf, "antigravity", "resources"),
				filepath.Join(pf, "Antigravity", "resources"),
				filepath.Join(pf, "Google", "Antigravity", "resources"),
			)
		}
		return dirs
	}
	return nil
}

func uniqueExistingDirs(candidates []string) []string {
	seen := make(map[string]bool, len(candidates))
	var out []string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		clean := filepath.Clean(c)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seen[key] {
			continue
		}
		if !isDir(clean) {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

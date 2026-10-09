package grokbot

import (
	"os"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func findGrok(candidates []string, lookups []func() (string, error)) (string, error) {
	for _, path := range candidates {
		if exe := grokExecutable(path); exe != "" {
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
		if exe := grokExecutable(path); exe != "" {
			return exe, nil
		}
	}
	return "", i18n.E("找不到 Grok Bot。请先安装，或从开始菜单打开一次。", "Grok Bot was not found. Install it, or open it once from the Start menu.")
}

// grokExecutable 把安装目录、卸载项图标或可执行文件路径解析成 Grok Bot.exe。
func grokExecutable(path string) string {
	path = cleanInstallPath(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		nested := filepath.Join(path, "Grok Bot.exe")
		if isFile(nested) {
			return nested
		}
		return ""
	}
	if strings.EqualFold(filepath.Base(path), "Grok Bot.exe") {
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
	return os.ExpandEnv(path)
}

func exeFromUninstall(displayName, installLocation, displayIcon string) string {
	if !strings.Contains(strings.ToLower(displayName), "grok bot") {
		return ""
	}
	if exe := grokExecutable(displayIcon); exe != "" {
		return exe
	}
	return grokExecutable(installLocation)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

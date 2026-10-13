//go:build windows

package codexbot

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cursor-inner/internal/winproc"
	"golang.org/x/sys/windows/registry"
)

func desktopCandidates() []string {
	local := os.Getenv("LOCALAPPDATA")
	return []string{
		filepath.Join(local, "Programs", "Codex", "Codex.exe"),
		filepath.Join(local, "Programs", "ChatGPT", "ChatGPT.exe"),
		filepath.Join(local, "Codex", "Codex.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Codex", "Codex.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Codex", "Codex.exe"),
	}
}

func desktopLookups() []func() (string, error) {
	return []func() (string, error){appxCodex, whereCodex, registryCodex}
}

// appxCodex 从当前用户的商店包仓库里找 OpenAI.Codex，效果同 Get-AppxPackage，但不用起 PowerShell。
// 仓库里可能留着旧版本的键，按包全名从新到旧试，取第一个目录里真有 exe 的。
func appxCodex() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return "", err
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return "", err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		if !strings.HasPrefix(strings.ToLower(name), "openai.codex_") {
			continue
		}
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		dir, _, _ := sub.GetStringValue("PackageRootFolder")
		sub.Close()
		if exe := codexExecutable(dir); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func whereCodex() (string, error) {
	for _, name := range []string{"Codex.exe", "ChatGPT.exe"} {
		for _, line := range winproc.Where(name) {
			if exe := codexExecutable(line); exe != "" {
				return exe, nil
			}
		}
	}
	return "", os.ErrNotExist
}

func registryCodex() (string, error) {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, name := range []string{`Software\Microsoft\Windows\CurrentVersion\App Paths\Codex.exe`, `Software\Microsoft\Windows\CurrentVersion\App Paths\ChatGPT.exe`} {
			if exe := appPath(root, name); exe != "" {
				return exe, nil
			}
		}
	}
	type hive struct {
		root   registry.Key
		access uint32
	}
	for _, item := range []hive{
		{registry.CURRENT_USER, registry.QUERY_VALUE | registry.ENUMERATE_SUB_KEYS},
		{registry.LOCAL_MACHINE, registry.QUERY_VALUE | registry.ENUMERATE_SUB_KEYS},
		{registry.LOCAL_MACHINE, registry.QUERY_VALUE | registry.ENUMERATE_SUB_KEYS | registry.WOW64_32KEY},
	} {
		if exe := uninstallCodex(item.root, item.access); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func appPath(root registry.Key, path string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return codexExecutable(value)
}

func uninstallCodex(root registry.Key, access uint32) string {
	k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, access)
	if err != nil {
		return ""
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return ""
	}
	for _, name := range names {
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		display, _, _ := sub.GetStringValue("DisplayName")
		location, _, _ := sub.GetStringValue("InstallLocation")
		icon, _, _ := sub.GetStringValue("DisplayIcon")
		sub.Close()
		if !codexDisplay(display) {
			continue
		}
		if exe := codexExecutable(icon); exe != "" {
			return exe
		}
		if exe := codexExecutable(location); exe != "" {
			return exe
		}
	}
	return ""
}

func codexDisplay(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "codex") || lower == "chatgpt"
}

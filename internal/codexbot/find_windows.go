//go:build windows

package codexbot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

func appxCodex() (string, error) {
	script := `$p = Get-AppxPackage -Name OpenAI.Codex -ErrorAction SilentlyContinue | Select-Object -First 1; if ($p) { $p.InstallLocation }`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", os.ErrNotExist
	}
	if exe := codexExecutable(dir); exe != "" {
		return exe, nil
	}
	return "", os.ErrNotExist
}

func whereCodex() (string, error) {
	for _, name := range []string{"Codex.exe", "ChatGPT.exe"} {
		out, err := exec.Command("where.exe", name).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(strings.TrimRight(line, "\r"))
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

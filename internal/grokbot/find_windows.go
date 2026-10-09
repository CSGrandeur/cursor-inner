//go:build windows

package grokbot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func windowsCandidates() []string {
	return []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Grok Bot", "Grok Bot.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Grok Bot", "Grok Bot.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Grok Bot", "Grok Bot.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Grok Bot", "Grok Bot.exe"),
	}
}

func whereGrok() (string, error) {
	out, err := exec.Command("where.exe", "Grok Bot.exe").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if exe := grokExecutable(line); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func registryGrok() (string, error) {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if exe := appPathGrok(root); exe != "" {
			return exe, nil
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
		if exe := uninstallGrok(item.root, item.access); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func appPathGrok(root registry.Key) string {
	k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\Grok Bot.exe`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return grokExecutable(value)
}

func uninstallGrok(root registry.Key, access uint32) string {
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
		if exe := exeFromUninstall(display, location, icon); exe != "" {
			return exe
		}
	}
	return ""
}

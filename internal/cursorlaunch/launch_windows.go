//go:build windows

package cursorlaunch

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"cursor-inner/internal/winproc"
)

// RunningExe 返回当前正在运行的 Cursor.exe 路径并记住，供账号切换关闭后再启动。
func RunningExe() string {
	procs, err := winproc.List("Cursor.exe")
	if err != nil {
		return LastRememberedExe()
	}
	for _, p := range procs {
		if exe := cursorExecutable(p.Exe); exe != "" {
			RememberExe(exe)
			return exe
		}
	}
	return LastRememberedExe()
}

func Start() error {
	path, err := findCursor(windowsCandidates(), lookupCursor)
	if err != nil {
		return err
	}
	RememberExe(path)
	cmd := winproc.GUI(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = withoutElectronNode(os.Environ())
	return cmd.Start()
}

func withoutElectronNode(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), "ELECTRON_RUN_AS_NODE=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func windowsCandidates() []string {
	var out []string
	if rem := LastRememberedExe(); rem != "" {
		out = append(out, rem)
	}
	if run := RunningExe(); run != "" {
		out = append(out, run)
	}
	out = append(out,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "cursor", "Cursor.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "cursor", "Cursor.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "cursor", "Cursor.exe"),
	)
	return out
}

func lookupCursor() (string, error) {
	for _, name := range []string{"Cursor.exe", "cursor"} {
		for _, line := range winproc.Where(name) {
			if exe := cursorExecutable(line); exe != "" {
				return exe, nil
			}
		}
	}
	return registryCursor()
}

func registryCursor() (string, error) {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\Cursor.exe`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := k.GetStringValue("")
		_ = k.Close()
		if err == nil {
			if exe := cursorExecutable(strings.Trim(os.ExpandEnv(strings.TrimSpace(value)), `"`)); exe != "" {
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
		if exe := uninstallCursor(item.root, item.access); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func uninstallCursor(root registry.Key, access uint32) string {
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
		_ = sub.Close()
		if exe := exeFromUninstall(display, os.ExpandEnv(location), os.ExpandEnv(icon)); exe != "" {
			return exe
		}
	}
	return ""
}

func exeFromUninstall(display, location, icon string) string {
	low := strings.ToLower(strings.TrimSpace(display))
	iconClean := strings.TrimSpace(icon)
	if i := strings.IndexByte(iconClean, ','); i >= 0 {
		iconClean = iconClean[:i]
	}
	iconClean = strings.Trim(strings.TrimSpace(iconClean), `"`)
	if low != "cursor" && !strings.HasPrefix(low, "cursor ") && !strings.Contains(strings.ToLower(iconClean), "cursor.exe") {
		return ""
	}
	if exe := cursorExecutable(iconClean); exe != "" {
		return exe
	}
	loc := strings.Trim(strings.TrimSpace(location), `"`)
	if loc != "" {
		if exe := cursorExecutable(filepath.Join(loc, "Cursor.exe")); exe != "" {
			return exe
		}
	}
	return ""
}

//go:build windows

package antigravitylaunch

import (
	"log/slog"
	"os"
	"path/filepath"

	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/winproc"
	"golang.org/x/sys/windows/registry"
)

// Launch 打开已安装的 Antigravity；已有进程在跑时也会激活或按需重启接管，不会静默吞掉。
// proxyURL 非空时带上与 Apply 相同的代理参数和环境变量（含 Go OAuth 认的 HTTPS_PROXY）。
func Launch(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return err
	}
	candidates := make([]string, 0, len(procs)+8)
	for _, p := range procs {
		if p.ExecutablePath != "" {
			candidates = append(candidates, p.ExecutablePath)
		}
	}
	candidates = append(candidates, windowsCandidates()...)
	path, err := findAntigravity(candidates, lookupAntigravity)
	if err != nil {
		return err
	}
	if len(procs) > 0 && grokbot.ShouldRestart(cmds(procs), proxyURL) {
		slog.Info("已有未接管的 Antigravity，先结束再以代理重启", "exe", path, "running", len(procs), logx.FileOnly)
		if err := stopAll(); err != nil {
			return err
		}
	} else {
		slog.Info("准备启动 Antigravity", "exe", path, "running", len(procs), logx.FileOnly)
	}
	return start(path, proxyURL)
}

func windowsCandidates() []string {
	var out []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		out = append(out,
			filepath.Join(local, "Programs", "antigravity", "Antigravity.exe"),
			filepath.Join(local, "Programs", "Antigravity", "Antigravity.exe"),
		)
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		out = append(out,
			filepath.Join(home, "AppData", "Local", "Programs", "antigravity", "Antigravity.exe"),
			filepath.Join(home, "AppData", "Local", "Programs", "Antigravity", "Antigravity.exe"),
		)
	}
	out = append(out,
		filepath.Join(os.Getenv("ProgramFiles"), "antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "antigravity", "Antigravity.exe"),
	)
	return out
}

func lookupAntigravity() (string, error) {
	if exe, err := whereAntigravity(); err == nil {
		return exe, nil
	}
	return registryAntigravity()
}

func whereAntigravity() (string, error) {
	for _, name := range []string{"Antigravity.exe", "antigravity"} {
		for _, line := range winproc.Where(name) {
			if exe := antigravityExecutable(line); exe != "" {
				return exe, nil
			}
		}
	}
	return "", os.ErrNotExist
}

func registryAntigravity() (string, error) {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if exe := appPathAntigravity(root); exe != "" {
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
		if exe := uninstallAntigravity(item.root, item.access); exe != "" {
			return exe, nil
		}
	}
	return "", os.ErrNotExist
}

func appPathAntigravity(root registry.Key) string {
	k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\Antigravity.exe`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	value, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return antigravityExecutable(os.ExpandEnv(value))
}

func uninstallAntigravity(root registry.Key, access uint32) string {
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
		if exe := exeFromUninstall(display, os.ExpandEnv(location), os.ExpandEnv(icon)); exe != "" {
			return exe
		}
	}
	return ""
}

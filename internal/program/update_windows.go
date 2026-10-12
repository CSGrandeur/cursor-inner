//go:build windows

package program

import (
	"errors"
	"os"
	"path/filepath"
)

// spawnDetached 拉起交接用的新进程：经典控制台窗口，与本进程生命周期无关（本进程退出不会带走它）。
// hidden 时不开控制台（新进程带着 --background，只挂通知区域图标）。
// 启动器很快退出不算失败；新进程是否真的起来由交接的 ready 超时判定。
func spawnDetached(exe string, hidden bool, args ...string) error {
	if hidden {
		err := launchDetached(append([]string{exe}, args...), classicConsoleCreationFlags())
		if errors.Is(err, errLauncherExited) {
			return nil
		}
		return err
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	err := launchDetached(append([]string{conhost, exe, "--classic-console"}, args...), classicConsoleCreationFlags())
	if err == nil || errors.Is(err, errLauncherExited) {
		return nil
	}
	err = launchDetached(append([]string{exe, "--classic-console"}, args...), fallbackConsoleCreationFlags())
	if errors.Is(err, errLauncherExited) {
		return nil
	}
	return err
}

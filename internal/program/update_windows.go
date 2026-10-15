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
//
// 每一种启动方式先带 CREATE_BREAKAWAY_FROM_JOB 试，作业不允许脱离时去掉它重试（launchRetry）。
// 顺序：conhost（脱离）→ conhost（不脱离）→ 直接起 exe（脱离）→ 直接起 exe（不脱离）。
// 全部失败时错误里列出每一次尝试，并把作业情况写进 inner.log。
func spawnDetached(exe string, hidden bool, args ...string) error {
	var errs []error
	try := func(argv []string, flags uint32) bool {
		err := launchRetry(argv, flags, launchDetached)
		if err == nil || errors.Is(err, errLauncherExited) {
			return true
		}
		errs = append(errs, err)
		return false
	}
	if hidden {
		if try(append([]string{exe}, args...), classicConsoleCreationFlags()) {
			return nil
		}
		return failSpawn(exe, errs)
	}
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	if try(append([]string{conhost, exe, "--classic-console"}, args...), classicConsoleCreationFlags()) {
		return nil
	}
	if try(append([]string{exe, "--classic-console"}, args...), fallbackConsoleCreationFlags()) {
		return nil
	}
	return failSpawn(exe, errs)
}

//go:build windows

package codexbot

import (
	"log/slog"

	"cursor-inner/internal/grokbot"
)

// Close 结束带着本程序代理参数的 Codex 桌面版（cursor-inner 退出时用），不重新打开。
func Close(proxyURL string) (closed bool, err error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return false, err
	}
	shells := desktopShells(procs)
	if len(shells) == 0 || !grokbot.OwnedBy(cmds(shells), proxyURL) {
		return false, nil
	}
	if err := stop(shells); err != nil {
		return false, err
	}
	slog.Info("cursor-inner 退出，已关闭接管的 Codex")
	return true, nil
}

// ManagedRunning 报告是否有带本程序代理参数的 Codex 桌面版在运行。
func ManagedRunning(proxyURL string) bool {
	procs, err := running()
	if err != nil {
		return false
	}
	shells := desktopShells(procs)
	return len(shells) > 0 && grokbot.OwnedBy(cmds(shells), proxyURL)
}

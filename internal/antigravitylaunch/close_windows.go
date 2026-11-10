//go:build windows

package antigravitylaunch

import (
	"log/slog"

	"cursor-inner/internal/grokbot"
)

// Close 结束带着本程序代理参数的 Antigravity（cursor-inner 退出时用），不重新打开。
func Close(proxyURL string) (closed bool, err error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listAnti()
	if err != nil {
		return false, err
	}
	if len(procs) == 0 || !grokbot.OwnedBy(cmds(procs), proxyURL) {
		return false, nil
	}
	if err := stopAll(); err != nil {
		return false, err
	}
	slog.Info("cursor-inner 退出，已关闭接管的 Antigravity")
	return true, nil
}

// ManagedRunning 报告是否有带本程序代理参数的 Antigravity 在运行。
func ManagedRunning(proxyURL string) bool {
	procs, err := listAnti()
	return err == nil && len(procs) > 0 && grokbot.OwnedBy(cmds(procs), proxyURL)
}

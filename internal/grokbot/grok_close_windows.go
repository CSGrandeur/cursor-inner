//go:build windows

package grokbot

import "log/slog"

// Close 结束带着本程序代理参数的 Grok Bot（cursor-inner 退出时用），不重新打开。
// 用户自己裸启动、没带我们参数的 Grok 不动。closed 表示确实关掉了。
func Close(proxyURL string) (closed bool, err error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listGrok()
	if err != nil {
		return false, err
	}
	if len(procs) == 0 || !OwnedBy(cmds(procs), proxyURL) {
		return false, nil
	}
	if err := stopAll(); err != nil {
		return false, err
	}
	slog.Info("cursor-inner 退出，已关闭接管的 Grok Bot")
	return true, nil
}

// ManagedRunning 报告是否有带本程序代理参数的 Grok Bot 在运行。
func ManagedRunning(proxyURL string) bool {
	procs, err := listGrok()
	return err == nil && len(procs) > 0 && OwnedBy(cmds(procs), proxyURL)
}

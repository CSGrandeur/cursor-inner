//go:build !windows

package grokbot

import "cursor-inner/internal/i18n"

// Launch 在非 Windows 上不可用：Grok Bot 的接管和启动只在 Windows 上实现。
func Launch(string) error {
	return i18n.E("启动 Grok 目前只支持 Windows。", "Launch Grok is currently only available on Windows.")
}

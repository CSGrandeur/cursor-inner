//go:build !windows

package codexbot

import "cursor-inner/internal/i18n"

func Launch(string) error {
	return i18n.E("启动 Codex 目前只支持 Windows。", "Launch Codex is currently only available on Windows.")
}

//go:build windows

package antiaccount

import (
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/winproc"
)

// 系统凭据库：目标 gemini:antigravity，用户 antigravity，原始 UTF-8 JSON。
// 进程内直调 Win32（winproc.CredWrite），不起 cmdkey / powershell 子进程。
const (
	credTarget = "gemini:antigravity"
	credUser   = "antigravity"
)

func keyringWrite(a Account) error {
	if err := winproc.CredWrite(credTarget, credUser, keyringPayload(a)); err != nil {
		return i18n.Wrap("写入 Windows 凭据库失败：", "Writing the Windows credential store failed: ", err)
	}
	return nil
}

func keyringRead() ([]byte, error) {
	return winproc.CredRead(credTarget)
}

func keyringReadInProcess() ([]byte, error) {
	return winproc.CredRead(credTarget)
}

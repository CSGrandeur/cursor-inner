//go:build windows

package grokbot

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Quiet 启动一个不弹出控制台的子进程。
// powershell、taskkill、where 都是控制台程序。接管开着时每 5 秒查一次 Grok 和 Codex，
// 不带这个标志就会各闪出一个黑框。CREATE_NO_WINDOW 仍能把标准输出接回管道。
func Quiet(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}

//go:build windows

package winproc

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows 构建里起子进程只能用本文件和 Quiet 这几个入口（源码门禁 TestWindowsBuildSpawnsOnlyThroughWinproc 守着）。
// 控制台程序（--version 自检、certutil 等）用 Quiet / QuietContext：不建控制台、不显示窗口。
// 图形程序（Cursor、Grok、Codex、资源管理器、浏览器）用 GUI：不能设 HideWindow，否则程序窗口也被藏起来；
// CREATE_NO_WINDOW 对图形程序无效、对误起的控制台程序则不再弹黑框。

// QuietContext 和 Quiet 一样，但跟随 ctx 结束进程。
func QuietContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}

// GUI 启动一个图形程序。
func GUI(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// Detached 启动一个与本进程脱离、没有控制台窗口的后台进程（守护进程）。
func Detached(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	return cmd
}

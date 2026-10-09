//go:build windows

package program

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleCtrlHandler = kernel32.NewProc("SetConsoleCtrlHandler")
	consoleCallback           uintptr
	consoleShutdown           func()
)

func executable() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path
}

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func startWatchdog(dir string) error {
	exe := executable()
	cmd := exec.Command(exe, "--watch", strconv.Itoa(os.Getpid()), "--data-dir", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	return cmd.Start()
}

func notifyAlreadyRunning(url string) {
	text := alreadyRunningText(url)
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	caption, _ := windows.UTF16PtrFromString("cursor-inner")
	body, _ := windows.UTF16PtrFromString(text)
	const mbOK = 0x00000000
	const mbIcon = 0x00000040
	const mbTop = 0x00040000
	const mbFront = 0x00010000
	_, _, _ = proc.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(caption)), mbOK|mbIcon|mbTop|mbFront)
	if url != "" {
		_ = openBrowser(url)
	}
}

func watchConsole(shutdown func()) {
	consoleShutdown = shutdown
	consoleCallback = syscall.NewCallback(onConsoleCtrl)
	_, _, _ = procSetConsoleCtrlHandler.Call(consoleCallback, 1)
}

func onConsoleCtrl(ctrl uintptr) uintptr {
	// 关闭按钮只收回通知区域。处理函数返回后，Windows 会结束仍挂在这个控制台上的控制台子系统进程；
	// 程序按窗口子系统构建，并先 FreeConsole，这样点叉不会结束进程。
	if consoleCloseAction(uint32(ctrl), trayReady.Load()) == closeHide && hideConsoleToTray() {
		return 1
	}
	switch uint32(ctrl) {
	case windows.CTRL_C_EVENT, windows.CTRL_BREAK_EVENT, windows.CTRL_LOGOFF_EVENT, windows.CTRL_SHUTDOWN_EVENT:
		if consoleShutdown != nil {
			consoleShutdown()
		}
		os.Exit(0)
	}
	return 0
}

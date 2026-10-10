//go:build windows

package program

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// spawnDetached 拉起交接用的新进程：经典控制台窗口，与本进程生命周期无关（本进程退出不会带走它）。
// 启动器很快退出不算失败；新进程是否真的起来由交接的 ready 超时判定。
func spawnDetached(exe string, args ...string) error {
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	err := launchDetached(append([]string{conhost, exe, "--classic-console"}, args...), classicConsoleCreationFlags())
	if err == nil || errors.Is(err, errLauncherExited) {
		return nil
	}
	err = launchDetached(append([]string{exe, "--classic-console"}, args...), fallbackConsoleCreationFlags())
	if errors.Is(err, errLauncherExited) {
		return nil
	}
	return err
}

func messageBox(text string, flags uintptr) uintptr {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	caption, _ := windows.UTF16PtrFromString("cursor-inner")
	body, _ := windows.UTF16PtrFromString(text)
	const mbTop = 0x00040000
	const mbFront = 0x00010000
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(caption)), flags|mbTop|mbFront)
	return r
}

func confirmDialog(text string) bool {
	const mbYesNo = 0x00000004
	const mbIconQuestion = 0x00000020
	const idYes = 6
	return messageBox(text, mbYesNo|mbIconQuestion) == idYes
}

func infoDialog(text string) {
	const mbIconInfo = 0x00000040
	messageBox(text, mbIconInfo)
}

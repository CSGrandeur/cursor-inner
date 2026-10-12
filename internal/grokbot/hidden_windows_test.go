//go:build windows

package grokbot

import (
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestQuietHidesTheConsole(t *testing.T) {
	cmd := Quiet("powershell.exe", "-NoProfile", "-Command", "exit 0")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("console window is still created")
	}
}

func TestQuietPowerShellHasNoWindow(t *testing.T) {
	cmd := Quiet("powershell.exe", "-NoProfile", "-Command", "Start-Sleep -Seconds 8")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if visibleWindow(cmd.Process.Pid) {
			t.Fatal("powershell opened a visible window")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func visibleWindow(pid int) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getPID := user32.NewProc("GetWindowThreadProcessId")
	isVisible := user32.NewProc("IsWindowVisible")
	var found bool
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var id uint32
		getPID.Call(hwnd, uintptr(unsafe.Pointer(&id)))
		if int(id) != pid {
			return 1
		}
		vis, _, _ := isVisible.Call(hwnd)
		if vis != 0 {
			found = true
		}
		return 1
	})
	enumWindows.Call(cb, 0)
	return found
}

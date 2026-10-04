//go:build windows

package takeover

import "golang.org/x/sys/windows"

func RunWatch(pid int, dir string) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
	recoverAfterExit(dir)
}

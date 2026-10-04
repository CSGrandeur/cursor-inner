//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTaskbarIdentityOnHiddenWindow(t *testing.T) {
	create := user32.NewProc("CreateWindowExW")
	destroy := user32.NewProc("DestroyWindow")
	class, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, err := create.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer destroy.Call(hwnd)

	setTaskbarIdentity(hwnd, `C:\Program Files\cursor-inner\cursor-inner.exe`)

	var store *propertyStore
	hr, _, _ := procSHGetPropertyStoreForWindow.Call(hwnd, uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&store)))
	if hr != 0 || store == nil {
		t.Fatalf("SHGetPropertyStoreForWindow hr=%#x", hr)
	}
	defer syscall.SyscallN(store.vtbl[2], uintptr(unsafe.Pointer(store)))
	for pid, want := range map[uint32]string{5: "CSGrandeur.cursor-inner", 3: `C:\Program Files\cursor-inner\cursor-inner.exe,0`} {
		key := propertyKey{fmtid: appUserModel, pid: pid}
		var v propVariant
		hr, _, _ := syscall.SyscallN(store.vtbl[5], uintptr(unsafe.Pointer(store)), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&v)))
		if hr != 0 || v.vt != 31 {
			t.Fatalf("pid %d: hr=%#x vt=%d", pid, hr, v.vt)
		}
		if got := windows.UTF16PtrToString(v.ptr); got != want {
			t.Fatalf("pid %d: %q, want %q", pid, got, want)
		}
	}
}

func TestConsoleProbesAreSafe(t *testing.T) {
	hwnd := consoleWindow()
	t.Logf("console window=%#x class=%q needsClassic=%v", hwnd, windowClass(hwnd), needsClassicConsole())
}

//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const classicEnv = "CURSOR_INNER_CLASSIC"

var (
	user32                          = windows.NewLazySystemDLL("user32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	procGetConsoleWindow            = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList       = kernel32.NewProc("GetConsoleProcessList")
	procSetConsoleTitleW            = kernel32.NewProc("SetConsoleTitleW")
	procGetClassNameW               = user32.NewProc("GetClassNameW")
	procSendMessageW                = user32.NewProc("SendMessageW")
	procExtractIconExW              = shell32.NewProc("ExtractIconExW")
	procSHGetPropertyStoreForWindow = shell32.NewProc("SHGetPropertyStoreForWindow")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
)

// needsClassicConsole 判断是否要改用经典控制台重开：控制台只属于本进程（双击、开始菜单、开机启动），
// 且由 Windows Terminal 托管。Windows Terminal 的任务栏按钮只能显示它自己的图标。
func needsClassicConsole() bool {
	if os.Getenv(classicEnv) == "1" {
		return false
	}
	hwnd := consoleWindow()
	if hwnd == 0 || windowClass(hwnd) != "PseudoConsoleWindow" {
		return false
	}
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

func startClassicConsole() error {
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	cmd := exec.Command(conhost, append([]string{executable()}, os.Args[1:]...)...)
	cmd.Env = append(os.Environ(), classicEnv+"=1")
	return cmd.Start()
}

func brandConsoleWindow() {
	if title, err := windows.UTF16PtrFromString("cursor-inner"); err == nil {
		_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(title)))
	}
	hwnd := consoleWindow()
	if hwnd == 0 || windowClass(hwnd) != "ConsoleWindowClass" {
		return
	}
	exe := executable()
	path, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return
	}
	var large, small uintptr
	if n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1); n > 0 {
		const wmSetIcon, iconSmall, iconBig = 0x0080, 0, 1
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconBig, large)
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
	setTaskbarIdentity(hwnd, exe)
}

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

type propVariant struct {
	vt  uint16
	_   [3]uint16
	ptr *uint16
	_   uintptr
}

type propertyStore struct {
	vtbl *[8]uintptr
}

var (
	iidPropertyStore = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}
	appUserModel     = windows.GUID{Data1: 0x9f4c2855, Data2: 0x9f79, Data3: 0x4b39, Data4: [8]byte{0xa8, 0xd0, 0xe1, 0xd4, 0x2d, 0xe1, 0xd5, 0xf3}}
)

// setTaskbarIdentity 给控制台窗口设置独立的 AppUserModelID 和图标资源，任务栏据此分组并显示 logo。
func setTaskbarIdentity(hwnd uintptr, exe string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _, _ = procCoInitializeEx.Call(0, 2)
	var store *propertyStore
	hr, _, _ := procSHGetPropertyStoreForWindow.Call(hwnd, uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&store)))
	if hr != 0 || store == nil {
		return
	}
	set := func(pid uint32, value string) {
		text, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return
		}
		key := propertyKey{fmtid: appUserModel, pid: pid}
		v := propVariant{vt: 31, ptr: text}
		_, _, _ = syscall.SyscallN(store.vtbl[6], uintptr(unsafe.Pointer(store)), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&v)))
		runtime.KeepAlive(text)
	}
	set(5, "CSGrandeur.cursor-inner")
	set(2, `"`+exe+`"`)
	set(3, exe+",0")
	set(4, "cursor-inner")
	_, _, _ = syscall.SyscallN(store.vtbl[7], uintptr(unsafe.Pointer(store)))
	_, _, _ = syscall.SyscallN(store.vtbl[2], uintptr(unsafe.Pointer(store)))
}

func consoleWindow() uintptr {
	hwnd, _, _ := procGetConsoleWindow.Call()
	return hwnd
}

func windowClass(hwnd uintptr) string {
	var buf [64]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

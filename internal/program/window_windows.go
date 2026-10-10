//go:build windows

package program

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errLauncherExited = errors.New("launcher exited")

var (
	user32                          = windows.NewLazySystemDLL("user32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	procGetConsoleWindow            = kernel32.NewProc("GetConsoleWindow")
	procAttachConsole               = kernel32.NewProc("AttachConsole")
	procGetConsoleProcessList       = kernel32.NewProc("GetConsoleProcessList")
	procSetConsoleTitleW            = kernel32.NewProc("SetConsoleTitleW")
	procGetClassNameW               = user32.NewProc("GetClassNameW")
	procSendMessageW                = user32.NewProc("SendMessageW")
	procExtractIconExW              = shell32.NewProc("ExtractIconExW")
	procSHGetPropertyStoreForWindow = shell32.NewProc("SHGetPropertyStoreForWindow")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
)

// handoffToClassicConsole 在双击或 Windows Terminal 里启动时，改由 conhost 打开经典控制台后退出当前进程。
// Windows Terminal 关闭标签页时会结束作业里的进程，FreeConsole 也拦不住。
func handoffToClassicConsole() bool {
	host := ""
	if consoleWindow() == 0 {
		const attachParent = ^uintptr(0)
		if r, _, _ := procAttachConsole.Call(attachParent); r != 0 {
			host = windowClass(consoleWindow())
		}
	} else {
		host = windowClass(consoleWindow())
	}
	switch consoleLaunchAction(classicConsole(os.Args[1:]), host) {
	case "stay":
		return false
	case "alloc":
		allocOwnConsole()
		return false
	default:
		if host != "" {
			_, _, _ = procFreeConsole.Call()
		}
		if startClassicConsole() == nil {
			return true
		}
		allocOwnConsole()
		return false
	}
}

func allocOwnConsole() {
	_, _, _ = procAllocConsole.Call()
}

func bindConsoleIO() {
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
	os.Stdout = out
	os.Stderr = out
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(in.Fd()))
	os.Stdin = in
}

// needsClassicConsole 判断是否要改用经典控制台重开：控制台只属于本进程（双击、开始菜单、开机启动），
// 且由 Windows Terminal 托管。Windows Terminal 的任务栏按钮只能显示它自己的图标。
func needsClassicConsole() bool {
	if classicConsole(os.Args[1:]) {
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

// classicConsoleCreationFlags 给 conhost 用：必须没有控制台（DETACHED_PROCESS）。
// CREATE_NEW_CONSOLE 会让 conhost 带着现成控制台启动，它随即退出且不运行命令，双击看起来像闪退。
// DETACHED_PROCESS 与 CREATE_NEW_CONSOLE 互斥。脱离作业是为了不随 Windows Terminal 的标签页一起结束。
func classicConsoleCreationFlags() uint32 {
	return windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NEW_PROCESS_GROUP
}

func fallbackConsoleCreationFlags() uint32 {
	return windows.CREATE_NEW_CONSOLE | windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NEW_PROCESS_GROUP
}

func startClassicConsole() error {
	exe := executable()
	extra := os.Args[1:]
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	err := launchDetached(append([]string{conhost, exe, "--classic-console"}, extra...), classicConsoleCreationFlags())
	if err == nil || errors.Is(err, errLauncherExited) {
		// conhost 的启动进程经常马上退出，真正的窗口还在。这时再拉起一份就会出现两个「已经在运行」。
		return nil
	}
	return launchDetached(append([]string{exe, "--classic-console"}, extra...), fallbackConsoleCreationFlags())
}

func launchDetached(argv []string, flags uint32) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, nil, nil, &si, &pi); err != nil {
		return err
	}
	defer func() {
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
	}()
	st, err := windows.WaitForSingleObject(pi.Process, 400)
	if err != nil {
		return err
	}
	if st == 0 { // WAIT_OBJECT_0：进程已退出
		var code uint32
		_ = windows.GetExitCodeProcess(pi.Process, &code)
		return fmt.Errorf("%w: %s code %d", errLauncherExited, argv[0], code)
	}
	return nil
}

func brandConsoleWindow() {
	if title, err := windows.UTF16PtrFromString(windowTitle); err == nil {
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

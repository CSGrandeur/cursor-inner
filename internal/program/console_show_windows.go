//go:build windows

package program

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// 控制台窗口的显示和隐藏（托盘菜单、配置页「后台运行」、关闭按钮）。
//
// 隐藏：经典控制台直接 SW_HIDE，进程仍挂在上面，再显示是瞬间的；别的宿主（例如 Windows Terminal）收不起来，
// 就 FreeConsole 拆下来。
// 显示：还挂着控制台就恢复窗口；已经没有控制台（后台启动、或者拆下来过）时，让 conhost 托管一个
// 只负责占住控制台的小进程（本程序的 --console-holder），再 AttachConsole 过去。直接 AllocConsole 在
// Windows 11 上会按「默认终端」开进 Windows Terminal，关标签页会连进程一起结束，关闭按钮也拦不住。

var (
	consoleMu     sync.Mutex
	holderEvent   windows.Handle
	procAttachPID = kernel32.NewProc("AttachConsole")
)

func holderEventName(owner int) string { return fmt.Sprintf(`Local\cursor-inner-console-%d`, owner) }

// hideConsole 收起控制台窗口，程序留在通知区域。
func hideConsole() {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	hwnd := consoleWindow()
	if hwnd == 0 {
		trayConcealed.Store(true)
		trayKick()
		return
	}
	if windowClass(hwnd) == "ConsoleWindowClass" {
		trayConcealed.Store(true)
		_, _, _ = procShowWindow.Call(hwnd, 0)
		trayKick()
		return
	}
	detachConsoleLocked()
	trayKick()
}

// hideConsoleToTray 在关闭按钮的处理函数里把进程从控制台拆下来。
// 处理函数返回后，Windows 会结束仍挂在该控制台上的进程；先 FreeConsole，关掉的就只是窗口。
func hideConsoleToTray() bool {
	if !trayReady.Load() {
		return false
	}
	consoleMu.Lock()
	detachConsoleLocked()
	consoleMu.Unlock()
	noteConcealed()
	return true
}

func detachConsoleLocked() {
	trayConcealed.Store(true)
	if residentTerm != nil {
		residentTerm.Detach()
	}
	if r, _, _ := procFreeConsole.Call(); r == 0 {
		if hwnd := consoleWindow(); hwnd != 0 {
			_, _, _ = procShowWindow.Call(hwnd, 0)
		}
	}
	releaseHolder()
}

// concealForExit 交接给新进程后，旧进程还要排空连接一阵子：先把窗口收掉，免得和新窗口同时出现。
func concealForExit() {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	if hwnd := consoleWindow(); hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, 0)
	}
	trayConcealed.Store(true)
}

func releaseHolder() {
	if holderEvent != 0 {
		_ = windows.SetEvent(holderEvent)
	}
}

// showConsole 打开（或恢复）控制台窗口。
func showConsole() {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	if hwnd := consoleWindow(); hwnd != 0 {
		trayConcealed.Store(false)
		const swRestore = 9
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
		_, _, _ = procSetForegroundWindow.Call(hwnd)
		trayKick()
		return
	}
	if !attachHolderConsole() {
		if r, _, _ := procAllocConsole.Call(); r == 0 {
			return
		}
	}
	trayConcealed.Store(false)
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return
	}
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(in.Fd()))
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
	os.Stdin = in
	os.Stdout = out
	os.Stderr = out
	brandConsoleWindow()
	if hwnd := consoleWindow(); hwnd != 0 {
		_, _, _ = procSetForegroundWindow.Call(hwnd)
	}
	if residentTerm != nil {
		residentTerm.Attach(out)
		if residentOpen != nil {
			open := func() { residentOpen("") }
			residentTerm.OnEnter(open)
			installConsoleLink(residentTerm, open)
		}
	}
	trayKick()
}

// attachHolderConsole 让 conhost 开一个经典控制台（里面跑 --console-holder），然后挂上去。
func attachHolderConsole() bool {
	exe := executable()
	if exe == "" {
		return false
	}
	name, err := windows.UTF16PtrFromString(holderEventName(os.Getpid()))
	if err != nil {
		return false
	}
	if holderEvent == 0 {
		holderEvent, err = windows.CreateEvent(nil, 1, 0, name)
		if err != nil && holderEvent == 0 {
			return false
		}
	}
	_ = windows.ResetEvent(holderEvent)
	pidFile := filepath.Join(os.TempDir(), fmt.Sprintf("cursor-inner-console-%d-%d.pid", os.Getpid(), time.Now().UnixNano()))
	defer os.Remove(pidFile)
	conhost := filepath.Join(os.Getenv("SystemRoot"), "System32", "conhost.exe")
	argv := []string{conhost, exe, "--console-holder", strconv.Itoa(os.Getpid()), pidFile}
	if err := launchDetached(argv, classicConsoleCreationFlags()); err != nil && !errors.Is(err, errLauncherExited) {
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && perr == nil && pid > 0 {
			if r, _, _ := procAttachPID.Call(uintptr(pid)); r != 0 {
				return true
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	releaseHolder()
	return false
}

// runConsoleHolder 是 --console-holder：占住 conhost 开的控制台，直到主进程退出或让它走。
func runConsoleHolder(owner int, pidFile string) {
	// 程序按窗口子系统构建，conhost 拉起时不会自动挂上控制台：和 --classic-console 一样先挂父进程的。
	if consoleWindow() == 0 {
		const attachParent = ^uintptr(0)
		if r, _, _ := procAttachPID.Call(attachParent); r == 0 {
			_, _, _ = procAllocConsole.Call()
		}
	}
	if consoleWindow() == 0 {
		return
	}
	const ignore = 1
	_, _, _ = procSetConsoleCtrlHandler.Call(0, ignore) // Ctrl+C 交给主进程处理
	tmp := pidFile + ".tmp"
	if os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600) == nil {
		_ = os.Rename(tmp, pidFile)
	}
	var handles []windows.Handle
	if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(owner)); err == nil {
		handles = append(handles, h)
	} else {
		return
	}
	if name, err := windows.UTF16PtrFromString(holderEventName(owner)); err == nil {
		if ev, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name); err == nil {
			handles = append(handles, ev)
		}
	}
	_, _ = windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
}

// traySupported：Windows 有通知区域图标，可以后台运行。
const traySupported = true

// concealConsoleAtStart 记下「后台启动、没有控制台」。
func concealConsoleAtStart() { trayConcealed.Store(true) }

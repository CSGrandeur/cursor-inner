//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"cursor-inner/internal/console"
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmNull    = 0x0000
	wmLButton = 0x0202
	wmRButton = 0x0205
	wmTray    = 0x8001
	trayUID   = 1

	cmdShow = 1
	cmdOpen = 2
	cmdQuit = 3
)

var (
	procShellNotifyIconW     = shell32.NewProc("Shell_NotifyIconW")
	procRegisterClassExW     = user32.NewProc("RegisterClassExW")
	procCreateWindowExW      = user32.NewProc("CreateWindowExW")
	procDefWindowProcW       = user32.NewProc("DefWindowProcW")
	procGetMessageW          = user32.NewProc("GetMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessageW     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	procAppendMenuW          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu       = user32.NewProc("TrackPopupMenu")
	procDestroyMenu          = user32.NewProc("DestroyMenu")
	procGetCursorPos         = user32.NewProc("GetCursorPos")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procShowWindow           = user32.NewProc("ShowWindow")
	procLoadIconW            = user32.NewProc("LoadIconW")
	procDestroyIcon          = user32.NewProc("DestroyIcon")
	procGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	procAllocConsole         = kernel32.NewProc("AllocConsole")
	procFreeConsole          = kernel32.NewProc("FreeConsole")
	procGetUserDefaultUILang = kernel32.NewProc("GetUserDefaultUILanguage")

	trayCallback   uintptr
	trayClassName  []uint16
	trayHwnd       atomic.Uintptr
	trayReady      atomic.Bool
	trayNoted      atomic.Bool
	trayConcealed  atomic.Bool
	trayIconHandle uintptr
	residentTerm   *console.Console
	residentOpen   func()
	residentQuit   func()
)

type wndClassEx struct {
	size      uint32
	style     uint32
	wndProc   uintptr
	clsExtra  int32
	wndExtra  int32
	instance  uintptr
	icon      uintptr
	cursor    uintptr
	brush     uintptr
	menuName  *uint16
	className *uint16
	iconSm    uintptr
}

type trayPoint struct {
	x int32
	y int32
}

type trayMsg struct {
	hwnd    uintptr
	message uint32
	wparam  uintptr
	lparam  uintptr
	time    uint32
	pt      trayPoint
}

type notifyIconData struct {
	cbSize    uint32
	hwnd      uintptr
	uid       uint32
	flags     uint32
	callback  uint32
	icon      uintptr
	tip       [128]uint16
	state     uint32
	stateMask uint32
	info      [256]uint16
	timeout   uint32
	version   uint32
	infoTitle [64]uint16
	infoFlags uint32
	guid      [16]byte
	balloon   uintptr
}

func prepareResident(term *console.Console, enabled bool, open, quit func()) {
	if !enabled {
		return
	}
	residentTerm = term
	residentOpen = open
	residentQuit = quit
	done := make(chan struct{})
	go runTray(done)
	<-done
}

func runTray(done chan struct{}) {
	runtime.LockOSThread()
	err := initTray()
	close(done)
	if err != nil {
		slog.Warn(fmt.Sprintf("通知区域图标没有挂上：%v", err))
		return
	}
	var m trayMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func initTray() error {
	if trayCallback == 0 {
		trayCallback = syscall.NewCallback(trayProc)
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	var err error
	if trayClassName == nil {
		trayClassName, err = windows.UTF16FromString("cursor-inner-tray")
		if err != nil {
			return err
		}
	}
	wc := wndClassEx{size: uint32(unsafe.Sizeof(wndClassEx{})), wndProc: trayCallback, instance: instance, className: &trayClassName[0]}
	if atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return errString("RegisterClassEx")
	}
	title, _ := windows.UTF16FromString("cursor-inner")
	const wsExTool = 0x00000080
	hwnd, _, _ := procCreateWindowExW.Call(wsExTool, uintptr(unsafe.Pointer(&trayClassName[0])), uintptr(unsafe.Pointer(&title[0])), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	runtime.KeepAlive(title)
	if hwnd == 0 {
		return errString("CreateWindowEx")
	}
	_, _, _ = procShowWindow.Call(hwnd, 0)
	trayHwnd.Store(hwnd)
	trayIconHandle = loadTrayIcon()
	if !shellNotify(nimAdd, 0x1|0x2|0x4, "") {
		return errNotify
	}
	trayReady.Store(true)
	return nil
}

var errNotify = errString("Shell_NotifyIcon")

type errString string

func (e errString) Error() string { return string(e) }

func trayProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmTray:
		switch lparam {
		case wmLButton:
			showFromTray()
		case wmRButton:
			showTrayMenu()
		}
		return 0
	case wmClose:
		removeTrayIcon()
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func showTrayMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	show, open, quit := "Show window", "Open settings", "Quit"
	if uiChinese() {
		show, open, quit = "显示窗口", "打开配置页", "退出"
	}
	appendMenu(menu, 0, cmdShow, show)
	appendMenu(menu, 0, cmdOpen, open)
	appendMenu(menu, 0x800, 0, "")
	appendMenu(menu, 0, cmdQuit, quit)
	var pt trayPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = procSetForegroundWindow.Call(trayHwnd.Load())
	const tpmReturn = 0x0100
	const tpmBottom = 0x0020
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturn|tpmBottom, uintptr(pt.x), uintptr(pt.y), 0, trayHwnd.Load(), 0)
	_, _, _ = procPostMessageW.Call(trayHwnd.Load(), wmNull, 0, 0)
	switch cmd {
	case cmdShow:
		showFromTray()
	case cmdOpen:
		if residentOpen != nil {
			residentOpen()
		}
	case cmdQuit:
		if residentQuit != nil {
			residentQuit()
		}
	}
}

func appendMenu(menu uintptr, flags uintptr, id uintptr, text string) {
	if text == "" {
		_, _, _ = procAppendMenuW.Call(menu, flags, id, 0)
		return
	}
	ptr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(ptr)))
	runtime.KeepAlive(ptr)
}

func uiChinese() bool {
	lang, _, _ := procGetUserDefaultUILang.Call()
	return uint16(lang)&0x3ff == 0x04
}

func loadTrayIcon() uintptr {
	exe := executable()
	path, err := windows.UTF16PtrFromString(exe)
	if err == nil {
		var large, small uintptr
		n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1)
		if n > 0 && int32(n) > 0 {
			if small != 0 {
				if large != 0 {
					_, _, _ = procDestroyIcon.Call(large)
				}
				return small
			}
			return large
		}
	}
	icon, _, _ := procLoadIconW.Call(0, 32512)
	return icon
}

func shellNotify(action uintptr, flags uint32, balloon string) bool {
	data := notifyIconData{
		cbSize:   uint32(unsafe.Sizeof(notifyIconData{})),
		hwnd:     trayHwnd.Load(),
		uid:      trayUID,
		flags:    flags,
		callback: wmTray,
		icon:     trayIconHandle,
	}
	copyUTF16(data.tip[:], "cursor-inner")
	if balloon != "" {
		data.flags |= 0x10
		data.infoFlags = 1
		title := "cursor-inner"
		copyUTF16(data.infoTitle[:], title)
		copyUTF16(data.info[:], balloon)
	}
	r, _, _ := procShellNotifyIconW.Call(action, uintptr(unsafe.Pointer(&data)))
	return r != 0
}

func copyUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	copy(dst, u)
}

func consoleHidden() bool { return trayConcealed.Load() }

func removeTrayIcon() {
	if trayHwnd.Load() == 0 {
		return
	}
	shellNotify(nimDelete, 0, "")
	trayReady.Store(false)
}

// hideConsoleToTray 在关闭按钮的处理函数里把进程从控制台拆下来。
// 处理函数返回后，Windows 会结束仍挂在该控制台上的进程；先 FreeConsole，关掉的就只是窗口。
func hideConsoleToTray() bool {
	if !trayReady.Load() {
		return false
	}
	trayConcealed.Store(true)
	if residentTerm != nil {
		residentTerm.Detach()
	}
	if r, _, _ := procFreeConsole.Call(); r == 0 {
		if hwnd := consoleWindow(); hwnd != 0 {
			_, _, _ = procShowWindow.Call(hwnd, 0)
		}
	}
	if trayNoted.CompareAndSwap(false, true) {
		text := "Still running in the notification area. Right-click the icon to quit."
		if uiChinese() {
			text = "仍在任务栏通知区域运行。右键图标可以选择退出。"
		}
		shellNotify(nimModify, 0x1|0x2|0x4, text)
	}
	return true
}

func showFromTray() {
	trayConcealed.Store(false)
	if hwnd := consoleWindow(); hwnd != 0 {
		const swRestore = 9
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
		_, _, _ = procSetForegroundWindow.Call(hwnd)
		return
	}
	if r, _, _ := procAllocConsole.Call(); r == 0 {
		return
	}
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
	if residentTerm != nil {
		residentTerm.Attach(out)
		if residentOpen != nil {
			residentTerm.OnEnter(residentOpen)
		}
	}
}

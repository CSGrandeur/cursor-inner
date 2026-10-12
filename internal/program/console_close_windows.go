//go:build windows

package program

import (
	"log/slog"
	"syscall"
	"unsafe"
)

const (
	whKeyboardLL = 13
	whMouseLL    = 14
	wmKeyDown    = 0x0100
	wmSysKeyDown = 0x0104
	wmMouseDown  = 0x0201
	wmMouseDbl   = 0x0203
	wmNCHitTest  = 0x0084
	gaRoot       = 2
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procWindowFromPoint     = user32.NewProc("WindowFromPoint")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetAncestor         = user32.NewProc("GetAncestor")

	consoleMouseHook uintptr
	consoleKeyHook   uintptr
	consoleMouseProc = syscall.NewCallback(consoleMouseHookProc)
	consoleKeyProc   = syscall.NewCallback(consoleKeyHookProc)
)

type msllHookStruct struct {
	x, y                   int32
	mouseData, flags, time uint32
	extra                  uintptr
}

type kbdllHookStruct struct {
	vkCode, scanCode, flags, time uint32
	extra                         uintptr
}

// installConsoleCloseHook 在关闭按钮的点击交给控制台之前拦下。
// 低级鼠标和键盘钩子跑在本进程，不需要注入到 conhost。
func installConsoleCloseHook() {
	mod, _, _ := procGetModuleHandleW.Call(0)
	if consoleMouseHook == 0 {
		consoleMouseHook, _, _ = procSetWindowsHookExW.Call(whMouseLL, consoleMouseProc, mod, 0)
	}
	if consoleKeyHook == 0 {
		consoleKeyHook, _, _ = procSetWindowsHookExW.Call(whKeyboardLL, consoleKeyProc, mod, 0)
	}
	if consoleMouseHook == 0 {
		slog.Warn("关闭按钮没有挂上，点叉仍会退出")
	}
}

func removeConsoleCloseHook() {
	if consoleMouseHook != 0 {
		_, _, _ = procUnhookWindowsHookEx.Call(consoleMouseHook)
		consoleMouseHook = 0
	}
	if consoleKeyHook != 0 {
		_, _, _ = procUnhookWindowsHookEx.Call(consoleKeyHook)
		consoleKeyHook = 0
	}
}

func consoleMouseHookProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) >= 0 && (wParam == wmMouseDown || wParam == wmMouseDbl) && trayReady.Load() && !trayConcealed.Load() && lParam != 0 {
		hs := (*msllHookStruct)(hookPointer(lParam))
		root := ancestorRoot(windowFromPoint(hs.x, hs.y))
		// 低级钩子看得到全桌面的点击：只拦本进程自己的控制台，别的经典控制台（包括另一份 cursor-inner）照常关闭。
		if root != 0 && root == consoleWindow() && concealCaptionClose(windowClass(root), hitTest(root, hs.x, hs.y)) {
			concealConsoleWindow(root)
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(consoleMouseHook, nCode, wParam, lParam)
	return r
}

func consoleKeyHookProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) >= 0 && (wParam == wmKeyDown || wParam == wmSysKeyDown) && trayReady.Load() && !trayConcealed.Load() && lParam != 0 {
		ks := (*kbdllHookStruct)(hookPointer(lParam))
		fg, _, _ := procGetForegroundWindow.Call()
		if fg != 0 && fg == consoleWindow() && concealAltF4(windowClass(fg), uintptr(ks.vkCode), uintptr(ks.flags)) {
			concealConsoleWindow(fg)
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(consoleKeyHook, nCode, wParam, lParam)
	return r
}

// hookPointer 把钩子回调里的地址交给结构体。直接把 uintptr 转成指针过不了 go vet。
func hookPointer(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

func concealConsoleWindow(hwnd uintptr) {
	if !trayReady.Load() || hwnd == 0 {
		return
	}
	trayConcealed.Store(true)
	_, _, _ = procShowWindow.Call(hwnd, 0)
	noteConcealed()
}

// noteConcealed 第一次点叉收起时提示一次：程序还在通知区域。
func noteConcealed() {
	if residentQuiet != nil && residentQuiet() {
		return
	}
	if !trayNoted.CompareAndSwap(false, true) {
		return
	}
	zh := uiChinese()
	trayBalloon(balloon{Title: "cursor-inner", Text: pick(zh, "仍在任务栏通知区域运行。单击图标打开配置页，右键可以退出。",
		"Still running in the notification area. Click the icon for settings; right-click to quit.")})
}

func windowFromPoint(x, y int32) uintptr {
	pt := uintptr(uint32(x)) | uintptr(uint32(y))<<32
	hwnd, _, _ := procWindowFromPoint.Call(pt)
	return hwnd
}

func ancestorRoot(hwnd uintptr) uintptr {
	if hwnd == 0 {
		return 0
	}
	root, _, _ := procGetAncestor.Call(hwnd, gaRoot)
	if root == 0 {
		return hwnd
	}
	return root
}

func hitTest(hwnd uintptr, x, y int32) uintptr {
	lp := uintptr(uint32(uint16(int16(x))) | uint32(uint16(int16(y)))<<16)
	hit, _, _ := procSendMessageW.Call(hwnd, wmNCHitTest, 0, lp)
	return hit
}

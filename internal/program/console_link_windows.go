//go:build windows

package program

import (
	"syscall"
	"unsafe"

	"cursor-inner/internal/console"

	"golang.org/x/sys/windows"
)

const (
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
)

var (
	procCallWindowProcW  = user32.NewProc("CallWindowProcW")
	consoleLinkCallback  = syscall.NewCallback(consoleLinkProc)
	consoleProcOrig      uintptr
	consoleLinkedHwnd    uintptr
	consoleClick         *console.Console
	openConsoleLink      func()
	linkSwallow          bool
	linkDownX, linkDownY int
	linkCellW, linkCellH int
)

// installConsoleLink 让经典控制台里单击顶栏地址就能打开。
// Windows Terminal 自己认 OSC 8 超链接；conhost 不认，单击会被快速编辑拿去选字。
func installConsoleLink(term *console.Console, open func()) {
	hwnd := consoleWindow()
	if hwnd == 0 || windowClass(hwnd) != "ConsoleWindowClass" || term == nil || open == nil {
		return
	}
	consoleClick = term
	openConsoleLink = open
	if hwnd == consoleLinkedHwnd && consoleProcOrig != 0 {
		return
	}
	index := int32(-4) // GWLP_WNDPROC
	prev, _, _ := procSetWindowLongPtr.Call(hwnd, uintptr(index), consoleLinkCallback)
	if prev == 0 {
		return
	}
	consoleProcOrig = prev
	consoleLinkedHwnd = hwnd
}

var (
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procGetClientRect    = user32.NewProc("GetClientRect")
)

func consoleLinkProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmLButtonDown:
		x, y := mousePoint(lparam)
		if urlAt(hwnd, x, y) != "" {
			linkSwallow = true
			linkDownX, linkDownY = x, y
			linkCellW, linkCellH = consoleCellSize(hwnd)
			return 0
		}
		linkSwallow = false
	case wmLButtonUp:
		if linkSwallow {
			linkSwallow = false
			x, y := mousePoint(lparam)
			if abs(x-linkDownX) <= linkCellW && abs(y-linkDownY) <= linkCellH {
				if urlAt(hwnd, x, y) != "" && openConsoleLink != nil {
					go openConsoleLink()
				}
			}
			return 0
		}
	}
	if consoleProcOrig == 0 {
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(consoleProcOrig, hwnd, msg, wparam, lparam)
	return r
}

func urlAt(hwnd uintptr, x, y int) string {
	col, row, ok := consoleCell(hwnd, x, y)
	if !ok {
		return ""
	}
	return consoleClick.LinkAt(col, row)
}

func mousePoint(lparam uintptr) (int, int) {
	return int(int16(lparam & 0xffff)), int(int16((lparam >> 16) & 0xffff))
}

func consoleCell(hwnd uintptr, x, y int) (col, row int, ok bool) {
	cw, ch := consoleCellSize(hwnd)
	if cw < 1 || ch < 1 || x < 0 || y < 0 {
		return 0, 0, false
	}
	return x / cw, y / ch, true
}

func consoleCellSize(hwnd uintptr) (int, int) {
	var rect struct{ left, top, right, bottom int32 }
	ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ok == 0 {
		return 0, 0
	}
	out, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0
	}
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(out, &info) != nil {
		return 0, 0
	}
	cols := int(info.Window.Right - info.Window.Left + 1)
	rows := int(info.Window.Bottom - info.Window.Top + 1)
	width := int(rect.right - rect.left)
	height := int(rect.bottom - rect.top)
	if cols < 1 || rows < 1 || width < cols || height < rows {
		return 0, 0
	}
	return width / cols, height / rows
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

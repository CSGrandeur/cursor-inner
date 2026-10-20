//go:build windows

package program

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"cursor-inner/internal/console"
)

// 通知区域图标。一个隐藏的顶层窗口（不是 message-only 窗口，否则收不到 TaskbarCreated 广播）
// 在专用线程上跑消息循环；Shell_NotifyIcon 的增删改都在这个线程上做，别的 goroutine 只投递消息。

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	niifInfo    = 0x1
	niifWarning = 0x2

	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmNull    = 0x0000
	wmTimer   = 0x0113

	trayLButtonUp   = 0x0202
	trayRButtonUp   = 0x0205
	trayBalloonUser = 0x0405 // NIN_BALLOONUSERCLICK

	wmTray        = 0x8001 // 图标回调
	wmTrayRefresh = 0x8002
	wmTrayAdd     = 0x8003
	wmTrayRemove  = 0x8004
	wmTrayBalloon = 0x8005

	trayUID   = 1
	trayTimer = 1
)

var (
	procShellNotifyIconW          = shell32.NewProc("Shell_NotifyIconW")
	procSHDefExtractIconW         = shell32.NewProc("SHDefExtractIconW")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu           = user32.NewProc("CreatePopupMenu")
	procAppendMenuW               = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem        = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu            = user32.NewProc("TrackPopupMenu")
	procDestroyMenu               = user32.NewProc("DestroyMenu")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procSendMessageTimeoutW       = user32.NewProc("SendMessageTimeoutW")
	procShowWindow                = user32.NewProc("ShowWindow")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procLoadIconW                 = user32.NewProc("LoadIconW")
	procDestroyIcon               = user32.NewProc("DestroyIcon")
	procSetTimer                  = user32.NewProc("SetTimer")
	procRegisterWindowMessageW    = user32.NewProc("RegisterWindowMessageW")
	procChangeWindowMessageFilter = user32.NewProc("ChangeWindowMessageFilterEx")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procGetModuleHandleW          = kernel32.NewProc("GetModuleHandleW")
	procAllocConsole              = kernel32.NewProc("AllocConsole")
	procFreeConsole               = kernel32.NewProc("FreeConsole")
	procGetUserDefaultUILang      = kernel32.NewProc("GetUserDefaultUILanguage")

	trayCallback   uintptr
	trayClassName  []uint16
	trayHwnd       atomic.Uintptr
	trayReady      atomic.Bool // 图标此刻在通知区域
	trayWanted     atomic.Bool // 应该显示图标（交接时要等确认后才显示）
	trayNoted      atomic.Bool
	trayConcealed  atomic.Bool // 控制台窗口被收起（或者后台运行、根本没开）
	trayTaskbarMsg uintptr     // Explorer 重启后广播的 TaskbarCreated

	// 只在托盘线程上读写
	trayIcons     [3]uintptr
	trayShownLvl  = trayLevel(-1)
	trayShownTip  string
	trayBaseIcon  uintptr
	trayIconBuilt bool

	trayMu      sync.Mutex
	trayPending []balloon

	residentTerm   *console.Console
	residentOpen   func(page string)
	trayClickPage  atomic.Value // 最近一条气泡要打开的标签
	residentQuit   func()
	residentUpdate func()
	residentFeed   func() trayInput
	residentQuiet  func() bool
)

// residentOptions 是托盘需要的东西。
type residentOptions struct {
	Term  *console.Console
	Open  func(page string) // 打开配置页；page 是标签（URL hash），空则默认标签
	Quit  func()
	Feed  func() trayInput
	Quiet func() bool // 配置里关了通知
	Defer bool        // 自更新交接的新进程：先不显示图标，交接确认后再 showTrayIcon（被回滚结束时不留幽灵图标）
}

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
	infoTitle [64]uint16
	infoFlags uint32
	guid      [16]byte
	balloon   uintptr
}

// prepareResident 挂上通知区域图标，返回是否挂上了。
func prepareResident(o residentOptions) bool {
	residentTerm = o.Term
	residentOpen = o.Open
	residentQuit = o.Quit
	residentFeed = o.Feed
	residentQuiet = o.Quiet
	trayWanted.Store(!o.Defer)
	done := make(chan error, 1)
	go runTray(done)
	if err := <-done; err != nil {
		slog.Warn(fmt.Sprintf("通知区域图标没有挂上：%v", err))
		return false
	}
	return true
}

func runTray(done chan<- error) {
	runtime.LockOSThread()
	if err := initTray(); err != nil {
		done <- err
		return
	}
	done <- nil
	installConsoleCloseHook()
	_, _, _ = procSetTimer.Call(trayHwnd.Load(), trayTimer, 1000, 0)
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
	if name, err := windows.UTF16PtrFromString("TaskbarCreated"); err == nil {
		trayTaskbarMsg, _, _ = procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
		if trayTaskbarMsg != 0 {
			// 以管理员身份运行时，UIPI 默认挡掉 Explorer（普通权限）发来的这条广播。
			const msgfltAllow = 1
			_, _, _ = procChangeWindowMessageFilter.Call(hwnd, trayTaskbarMsg, msgfltAllow, 0)
		}
	}
	if trayWanted.Load() && !addTrayIcon() {
		return errNotify
	}
	return nil
}

var errNotify = errString("Shell_NotifyIcon")

type errString string

func (e errString) Error() string { return string(e) }

func trayProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if trayTaskbarMsg != 0 && msg == trayTaskbarMsg {
		// Explorer 重启（或崩溃后被拉起）：旧图标随它一起没了，重新加。
		trayReady.Store(false)
		if trayWanted.Load() {
			addTrayIcon()
		}
		return 0
	}
	switch msg {
	case wmTray:
		switch lparam & 0xffff {
		case trayLButtonUp:
			if residentOpen != nil {
				residentOpen("")
			}
		case trayBalloonUser:
			if residentOpen != nil {
				page, _ := trayClickPage.Load().(string)
				residentOpen(page)
			}
		case trayRButtonUp:
			showTrayMenu()
		}
		return 0
	case wmTimer, wmTrayRefresh:
		refreshTray()
		return 0
	case wmTrayAdd:
		trayWanted.Store(true)
		if !trayReady.Load() {
			addTrayIcon()
		}
		return 0
	case wmTrayRemove:
		trayWanted.Store(false)
		if trayReady.Load() {
			shellNotify(nimDelete, 0, nil)
			trayReady.Store(false)
		}
		return 0
	case wmTrayBalloon:
		trayMu.Lock()
		pending := trayPending
		trayPending = nil
		trayMu.Unlock()
		for _, b := range pending {
			if trayReady.Load() {
				trayClickPage.Store(b.Page)
				shellNotify(nimModify, 0, &b)
			}
		}
		return 0
	case wmClose:
		trayWanted.Store(false)
		if trayReady.Load() {
			shellNotify(nimDelete, 0, nil)
			trayReady.Store(false)
		}
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// addTrayIcon 在托盘线程上把图标加进通知区域。
func addTrayIcon() bool {
	trayShownLvl, trayShownTip = trayLook(feedNow())
	if !shellNotify(nimAdd, nifMessage|nifIcon|nifTip, nil) {
		return false
	}
	trayReady.Store(true)
	return true
}

// refreshTray 只在图标或提示文字变化时才通知 Explorer。
func refreshTray() {
	if !trayReady.Load() {
		return
	}
	lvl, tip := trayLook(feedNow())
	if lvl == trayShownLvl && tip == trayShownTip {
		return
	}
	trayShownLvl, trayShownTip = lvl, tip
	shellNotify(nimModify, nifIcon|nifTip, nil)
}

func feedNow() trayInput {
	if residentFeed != nil {
		return residentFeed()
	}
	return trayInput{Version: "", Zh: uiChinese()}
}

func shellNotify(action uintptr, flags uint32, b *balloon) bool {
	data := notifyIconData{
		cbSize:   uint32(unsafe.Sizeof(notifyIconData{})),
		hwnd:     trayHwnd.Load(),
		uid:      trayUID,
		flags:    flags,
		callback: wmTray,
	}
	if flags&nifIcon != 0 {
		data.icon = trayIcon(trayShownLvl)
	}
	if flags&nifTip != 0 {
		copyUTF16(data.tip[:], trayShownTip)
	}
	if b != nil {
		data.flags |= nifInfo
		data.infoFlags = niifInfo
		if b.Warn {
			data.infoFlags = niifWarning
		}
		copyUTF16(data.infoTitle[:], truncUTF16(b.Title, len(data.infoTitle)-1))
		copyUTF16(data.info[:], truncUTF16(b.Text, len(data.info)-1))
	}
	r, _, _ := procShellNotifyIconW.Call(action, uintptr(unsafe.Pointer(&data)))
	return r != 0
}

// trayIcon 返回某个状态的图标：正常是程序图标，警告、更新中在右下角加圆点。建一次缓存起来。
func trayIcon(level trayLevel) uintptr {
	if !trayIconBuilt {
		trayIconBuilt = true
		size := smallIconSize()
		trayBaseIcon = loadTrayIcon(size)
		trayIcons[trayNormal] = trayBaseIcon
		for _, lvl := range []trayLevel{trayWarn, trayUpdating} {
			trayIcons[lvl] = trayBaseIcon
			c, ok := badgeColor(lvl)
			if !ok || trayBaseIcon == 0 {
				continue
			}
			if px, ok := iconPixels(trayBaseIcon, size); ok {
				if icon := iconFromPixels(badgePixels(px, size, c), size); icon != 0 {
					trayIcons[lvl] = icon
				}
			}
		}
	}
	if level < 0 || int(level) >= len(trayIcons) {
		level = trayNormal
	}
	return trayIcons[level]
}

func smallIconSize() int {
	const smCxSmIcon = 49
	n, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if int(n) < 16 || int(n) > 256 {
		return 16
	}
	return int(n)
}

// loadTrayIcon 按通知区域的尺寸从程序文件里取图标；取不到用系统默认图标。
func loadTrayIcon(size int) uintptr {
	path, err := windows.UTF16PtrFromString(executable())
	if err == nil {
		var icon uintptr
		sz := uintptr(size) | uintptr(size)<<16
		if hr, _, _ := procSHDefExtractIconW.Call(uintptr(unsafe.Pointer(path)), 0, 0, uintptr(unsafe.Pointer(&icon)), 0, sz); hr == 0 && icon != 0 {
			return icon
		}
		var large, small uintptr
		if n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1); int32(n) > 0 {
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

func showTrayMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	shown := consoleVisible()
	items := trayMenu(uiChinese(), shown, residentUpdate != nil, updateBusy(feedNow().Update.State))
	for _, it := range items {
		switch {
		case it.Sep:
			appendMenu(menu, 0x800, 0, "")
		case it.Disabled:
			appendMenu(menu, 0x1, it.ID, it.Text) // MF_GRAYED
		default:
			appendMenu(menu, 0, it.ID, it.Text)
		}
		if it.Default {
			_, _, _ = procSetMenuDefaultItem.Call(menu, it.ID, 0)
		}
	}
	var pt trayPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = procSetForegroundWindow.Call(trayHwnd.Load())
	const tpmReturn = 0x0100
	const tpmBottom = 0x0020
	const tpmRight = 0x0008
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturn|tpmBottom|tpmRight, uintptr(pt.x), uintptr(pt.y), 0, trayHwnd.Load(), 0)
	_, _, _ = procPostMessageW.Call(trayHwnd.Load(), wmNull, 0, 0)
	trayDispatch(cmd, shown, trayActions{
		Open:        residentOpen,
		ShowConsole: func() { go showConsole() }, // 可能要等新控制台起来，不能卡住消息循环
		HideConsole: hideConsole,
		Check:       residentUpdate,
		Quit:        residentQuit,
	})
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

func copyUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func postTray(msg uintptr) {
	if hwnd := trayHwnd.Load(); hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, msg, 0, 0)
	}
}

// showTrayIcon 在交接确认后显示图标。
func showTrayIcon() { postTray(wmTrayAdd) }

// trayKick 让托盘马上刷新一次（不用等定时器）。
func trayKick() { postTray(wmTrayRefresh) }

// trayBalloon 弹一条气泡通知。
func trayBalloon(b balloon) {
	if trayHwnd.Load() == 0 {
		return
	}
	trayMu.Lock()
	trayPending = append(trayPending, b)
	trayMu.Unlock()
	postTray(wmTrayBalloon)
}

func trayAvailable() bool { return trayHwnd.Load() != 0 }

func consoleHidden() bool { return trayConcealed.Load() }

// removeTrayIcon 同步删掉图标：退出、交接给新进程时调用，返回时图标已经不在了。
func removeTrayIcon() {
	removeConsoleCloseHook()
	hwnd := trayHwnd.Load()
	if hwnd == 0 {
		return
	}
	const smtoAbortIfHung = 0x2
	var res uintptr
	_, _, _ = procSendMessageTimeoutW.Call(hwnd, wmTrayRemove, 0, 0, smtoAbortIfHung, uintptr((2 * time.Second).Milliseconds()), uintptr(unsafe.Pointer(&res)))
}

// setTrayUpdate 挂上托盘菜单“检查更新”。
func setTrayUpdate(fn func()) { residentUpdate = fn }

func consoleVisible() bool {
	hwnd := consoleWindow()
	if hwnd == 0 {
		return false
	}
	v, _, _ := procIsWindowVisible.Call(hwnd)
	return v != 0
}

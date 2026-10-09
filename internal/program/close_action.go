package program

const ctrlCloseEvent uint32 = 2

type closeAction int

const (
	closeQuit closeAction = iota
	closeHide
)

// consoleLaunchAction 决定这次进程要不要换到经典控制台。
// host 为空表示还没有控制台；PseudoConsoleWindow 是 Windows Terminal。
// 返回 relaunch（交给 conhost 后当前进程退出）、alloc（自己建控制台）或 stay。
func consoleLaunchAction(classicArg bool, host string) string {
	if classicArg {
		if host == "" {
			return "alloc"
		}
		return "stay"
	}
	if host == "" || host == "PseudoConsoleWindow" {
		return "relaunch"
	}
	return "stay"
}

const (
	hitClose     uintptr = 20
	vkF4         uintptr = 0x73
	llkhfAltDown uintptr = 0x20
	llkhfUp      uintptr = 0x80
)

// concealCaptionClose 判断这次点击是不是经典控制台标题栏的关闭按钮。
// CTRL_CLOSE_EVENT 取消不了，系统会在处理函数返回后结束进程，所以要点击到达控制台之前拦下。
func concealCaptionClose(class string, hit uintptr) bool {
	return class == "ConsoleWindowClass" && hit == hitClose
}

// concealAltF4 判断前台是不是经典控制台，并且按下了 Alt+F4。
func concealAltF4(class string, vk, flags uintptr) bool {
	return class == "ConsoleWindowClass" && vk == vkF4 && flags&llkhfAltDown != 0 && flags&llkhfUp == 0
}

// consoleCloseAction 决定控制台控制事件是退出还是收回通知区域。
// 只有点窗口关闭按钮，并且通知区域图标已经挂上时，才收回。
func consoleCloseAction(ctrl uint32, resident bool) closeAction {
	if resident && ctrl == ctrlCloseEvent {
		return closeHide
	}
	return closeQuit
}

package main

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

// consoleCloseAction 决定控制台控制事件是退出还是收回通知区域。
// 只有点窗口关闭按钮，并且通知区域图标已经挂上时，才收回。
func consoleCloseAction(ctrl uint32, resident bool) closeAction {
	if resident && ctrl == ctrlCloseEvent {
		return closeHide
	}
	return closeQuit
}

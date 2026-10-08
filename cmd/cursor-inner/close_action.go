package main

const ctrlCloseEvent uint32 = 2

type closeAction int

const (
	closeQuit closeAction = iota
	closeHide
)

// consoleCloseAction 决定控制台控制事件是退出还是收回通知区域。
// 只有点窗口关闭按钮，并且通知区域图标已经挂上时，才收回。
func consoleCloseAction(ctrl uint32, resident bool) closeAction {
	if resident && ctrl == ctrlCloseEvent {
		return closeHide
	}
	return closeQuit
}

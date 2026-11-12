package takeover

// terminateCursor 是结束 Cursor 的实际入口，测试可替换，用来断言
// 已接管恢复和热更新路径绝不会走到这里。
var terminateCursor = terminateCursorPlatform

// TerminateCursor 结束 Cursor 进程，让新的代理设置在重新打开后生效。
// 首次接管、用户关闭接管而还原设置、以及守护进程在非交接退出后的恢复会走这里。
// 已接管状态下的重启恢复（Resume/Adopt）和自更新交接不得调用。
func TerminateCursor() error {
	return terminateCursor()
}

//go:build !windows

package grokbot

// closeWaitCount 只在 Windows 上实现（GetExtendedTcpTable）；其它系统返回 -1。
func closeWaitCount() int { return -1 }

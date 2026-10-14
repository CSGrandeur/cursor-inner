//go:build !windows

package takeover

// CursorRunning 只在 Windows 上用于退出确认框；其它平台不列出 Cursor。
func CursorRunning() bool { return false }

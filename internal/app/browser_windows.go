//go:build windows

package app

import "cursor-inner/internal/winproc"

// openBrowserURL 用系统默认浏览器打开链接（用户点「添加账号」才触发，一次性操作）。
func openBrowserURL(url string) error {
	return winproc.GUI("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

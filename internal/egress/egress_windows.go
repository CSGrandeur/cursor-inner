//go:build windows

package egress

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Start 把脚本写到用户临时目录，再以管理员身份（UAC 弹窗）打开 PowerShell 执行。立即返回，
// 结果显示在那个 PowerShell 窗口里；用户拒绝 UAC 时什么都不改。
func Start(enable bool, proxyURL string) error {
	path := filepath.Join(os.TempDir(), "cursor-inner-strict-egress.ps1")
	if err := os.WriteFile(path, Script, 0o600); err != nil {
		return err
	}
	args, err := Arguments(path, enable, proxyURL)
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	exe, _ := windows.UTF16PtrFromString("powershell.exe")
	params, _ := windows.UTF16PtrFromString(args)
	return windows.ShellExecute(0, verb, exe, params, nil, windows.SW_SHOWNORMAL)
}

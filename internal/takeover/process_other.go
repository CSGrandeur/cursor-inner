//go:build !windows

package takeover

import "errors"

func settingsPath() (string, error) {
	return "", errors.New("接管只在 Windows 上写入 Cursor 配置")
}

func TerminateCursor() error {
	return errors.New("结束 Cursor 只在 Windows 上执行")
}

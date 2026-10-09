//go:build windows

package procfwd

import (
	"errors"
	"os"

	"cursor-inner/internal/i18n"

	"golang.org/x/sys/windows"
)

// commitHosts 直接改原来的 hosts。
// drivers\etc 允许管理员改已有文件，但不允许在目录里新建文件再替换：
// MoveFileEx 会返回 Access is denied，即使用户是用「以管理员身份运行」打开的。
func commitHosts(path string, data []byte) error {
	if err := clearReadOnly(path); err != nil && !os.IsNotExist(err) {
		return annotateHosts(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return annotateHosts(err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return annotateHosts(werr)
	}
	return annotateHosts(cerr)
}

func clearReadOnly(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return nil
	}
	return windows.SetFileAttributes(p, attrs&^windows.FILE_ATTRIBUTE_READONLY)
}

func annotateHosts(err error) error {
	if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	if elevated() {
		return i18n.Ef("%s（进程已是管理员，系统仍拒绝写入 hosts）", "%s (the process is elevated, and Windows still refused the hosts file)", err.Error())
	}
	return i18n.Ef("%s（当前进程没有管理员权限）", "%s (the process is not running as administrator)", err.Error())
}

func elevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

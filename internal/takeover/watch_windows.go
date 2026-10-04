//go:build windows

package takeover

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func RunWatch(pid int, dir string) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
	if !Marked(dir) {
		return
	}
	err = Recover(dir)
	logWatch(dir, err)
}

func logWatch(dir string, err error) {
	line := time.Now().Format(time.RFC3339) + " 主进程已退出，已撤掉接管并结束 Cursor\n"
	if err != nil {
		line = time.Now().Format(time.RFC3339) + " 主进程已退出，撤掉接管失败：" + err.Error() + "\n"
	}
	_ = os.MkdirAll(dir, 0o755)
	f, openErr := os.OpenFile(filepath.Join(dir, "watch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

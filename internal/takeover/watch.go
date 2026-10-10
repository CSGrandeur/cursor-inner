package takeover

import (
	"os"
	"path/filepath"
	"time"

	"cursor-inner/internal/selfupdate"
)

func recoverAfterExit(dir string, pid int) {
	if !Marked(dir) {
		return
	}
	// 自更新交接：旧进程退出是把接管交给了新进程，新进程有自己的守护进程，这里什么都不做。
	if selfupdate.HandedOver(dir, pid) {
		return
	}
	err := Recover(dir)
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

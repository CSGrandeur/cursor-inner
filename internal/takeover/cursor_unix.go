//go:build darwin || linux

package takeover

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"cursor-inner/internal/i18n"
)

// cursorMatch 是 pgrep/pkill 的匹配参数。macOS 按 Cursor.app 内的可执行文件路径匹配，
// 覆盖主进程和各个 Helper；Linux 按进程名 cursor 精确匹配，不会命中 cursor-inner。
func cursorMatch() []string {
	if runtime.GOOS == "darwin" {
		return []string{"-f", `Cursor\.app/Contents/`}
	}
	return []string{"-x", "cursor"}
}

func TerminateCursor() error {
	if !cursorRunning() {
		return nil
	}
	signalCursor("-TERM")
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !cursorRunning() {
			return nil
		}
	}
	signalCursor("-KILL")
	time.Sleep(500 * time.Millisecond)
	if cursorRunning() {
		return i18n.E("Cursor 进程仍在运行", "Cursor is still running")
	}
	return nil
}

func cursorRunning() bool {
	args := append([]string{"-u", strconv.Itoa(os.Getuid())}, cursorMatch()...)
	return exec.Command("pgrep", args...).Run() == nil
}

func signalCursor(sig string) {
	args := append([]string{sig, "-u", strconv.Itoa(os.Getuid())}, cursorMatch()...)
	_ = exec.Command("pkill", args...).Run()
}

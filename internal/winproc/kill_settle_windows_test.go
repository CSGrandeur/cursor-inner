//go:build windows

package winproc

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 主进程被结束后，Electron 子进程正在自己退出：结束它会得到“拒绝访问”，
// 但它很快就退出了。这种情况不能算失败，否则调用方不会再重新打开程序。
func TestKillTreatsAccessDeniedOnExitingProcessAsDone(t *testing.T) {
	exitsAfter := 50 * time.Millisecond
	exited := func(wait time.Duration) bool { return wait >= exitsAfter }
	if err := settle(windows.ERROR_ACCESS_DENIED, exited); err != nil {
		t.Fatalf("进程很快退出了，不应报错：%v", err)
	}
}

func TestKillStillReportsProcessThatStaysAlive(t *testing.T) {
	never := func(time.Duration) bool { return false }
	if err := settle(windows.ERROR_ACCESS_DENIED, never); err == nil {
		t.Fatal("进程一直没退出，必须报错")
	}
}

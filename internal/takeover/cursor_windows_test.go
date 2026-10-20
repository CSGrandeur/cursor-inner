//go:build windows

package takeover

import (
	"testing"

	"cursor-inner/internal/winproc/winproctest"
)

// 复现 watch.log 的“撤掉接管失败：exit status 0xc0000142”：关机时起 taskkill 子进程会失败。
// 现在在进程内结束，不起任何子进程；且只结束 Cursor.exe 本身（不带子进程树）。
func TestTerminateCursorKillsInProcessWithoutChildren(t *testing.T) {
	oldList, oldKill := cursorPIDs, killCursorPID
	t.Cleanup(func() { cursorPIDs, killCursorPID = oldList, oldKill })
	cursorPIDs = func() ([]uint32, error) { return []uint32{11, 22}, nil }
	var killed []uint32
	killCursorPID = func(pid uint32) error { killed = append(killed, pid); return nil }
	winproctest.NoChildren(t, func() {
		if err := TerminateCursor(); err != nil {
			t.Fatal(err)
		}
		// NoChildren 在子测试进程里跑 fn，断言也要放在里面。
		if len(killed) != 2 || killed[0] != 11 || killed[1] != 22 {
			t.Fatalf("killed %v", killed)
		}
	})
}

func TestTerminateCursorNothingRunningIsFine(t *testing.T) {
	oldList, oldKill := cursorPIDs, killCursorPID
	t.Cleanup(func() { cursorPIDs, killCursorPID = oldList, oldKill })
	cursorPIDs = func() ([]uint32, error) { return nil, nil }
	killCursorPID = func(uint32) error { t.Fatal("no kill expected"); return nil }
	if err := TerminateCursor(); err != nil || CursorRunning() {
		t.Fatalf("err=%v running=%v", err, CursorRunning())
	}
}

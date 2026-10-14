//go:build windows

package takeover

import (
	"cursor-inner/internal/winproc"
)

// 测试替换用。生产路径在进程内列出并结束 Cursor.exe，不起 taskkill 子进程：
// 关机、注销时桌面已在拆除，新起的控制台程序会以 0xc0000142（DLL 初始化失败）退出，
// 守护进程因此撤不掉接管（watch.log 里多次出现）。
var (
	cursorPIDs = func() ([]uint32, error) {
		procs, err := winproc.List("Cursor.exe")
		if err != nil {
			return nil, err
		}
		out := make([]uint32, 0, len(procs))
		for _, p := range procs {
			out = append(out, p.PID)
		}
		return out, nil
	}
	// 只结束 Cursor.exe 自己，不带子进程：Remote WSL 的 wsl.exe 是 Cursor 的子进程，
	// 连带结束会让 mirrored 模式下的 WSLService 返回 E_UNEXPECTED。
	killCursorPID = winproc.Kill
)

// TerminateCursor 结束所有 Cursor.exe。没在运行不算错。
func TerminateCursor() error {
	pids, err := cursorPIDs()
	if err != nil {
		return err
	}
	var first error
	for _, pid := range pids {
		if err := killCursorPID(pid); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CursorRunning 报告 Cursor 是否在运行（退出确认框用）。
func CursorRunning() bool {
	pids, err := cursorPIDs()
	return err == nil && len(pids) > 0
}

//go:build darwin || linux

package takeover

import (
	"syscall"
	"time"
)

func RunWatch(pid int, dir string) {
	for syscall.Kill(pid, 0) == nil {
		time.Sleep(300 * time.Millisecond)
	}
	recoverAfterExit(dir, pid)
}

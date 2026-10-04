//go:build !windows

package takeover

import (
	"os"
	"syscall"
	"time"
)

func RunWatch(pid int, dir string) {
	for alive(pid) {
		time.Sleep(300 * time.Millisecond)
	}
	_ = Recover(dir)
}

func alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

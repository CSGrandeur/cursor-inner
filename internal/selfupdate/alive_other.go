//go:build !windows

package selfupdate

import "syscall"

// ProcessAlive 判断进程是否仍在运行。
func ProcessAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// KillProcess 结束进程。
func KillProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

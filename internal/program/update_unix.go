//go:build darwin || linux

package program

import (
	"os/exec"
	"syscall"
)

func spawnDetached(exe string, _ bool, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// 非 Windows 没有托盘菜单。
func setTrayUpdate(func()) {}
func uiChinese() bool      { return true }

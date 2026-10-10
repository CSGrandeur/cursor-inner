//go:build darwin || linux

package program

import (
	"log"
	"os/exec"
	"syscall"
)

func spawnDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// 非 Windows 没有托盘菜单；确认只在配置页里做。
func confirmDialog(text string) bool { log.Print(text); return false }
func infoDialog(text string)         { log.Print(text) }
func setTrayUpdate(func())           {}
func uiChinese() bool                { return true }

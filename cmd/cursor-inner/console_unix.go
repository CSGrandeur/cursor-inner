//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p
}

func watchConsole(func()) {}

func startWatchdog(dir string) error {
	cmd := exec.Command(executable(), "--watch", strconv.Itoa(os.Getpid()), "--data-dir", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

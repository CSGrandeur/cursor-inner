//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func openBrowser(url string) error {
	if cmd, err := exec.LookPath("cmd.exe"); err == nil {
		return exec.Command(cmd, "/c", "start", url).Start()
	}
	if cmd, err := exec.LookPath("xdg-open"); err == nil {
		return exec.Command(cmd, url).Start()
	}
	return nil
}

func watchConsole(func()) {}

func notifyAlreadyRunning(url string) {
	if url != "" {
		fmt.Fprintln(os.Stderr, "cursor-inner 已在运行：", url)
		_ = openBrowser(url)
		return
	}
	fmt.Fprintln(os.Stderr, "cursor-inner 已在运行")
}

func startWatchdog() error {
	exe := executable()
	cmd := exec.Command(exe, "--watch", strconv.Itoa(os.Getpid()))
	return cmd.Start()
}

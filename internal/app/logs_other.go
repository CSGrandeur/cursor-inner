//go:build !windows

package app

import (
	"os/exec"
	"runtime"
)

func folderCommand(dir string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", dir)
	}
	return exec.Command("xdg-open", dir)
}

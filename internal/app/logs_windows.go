//go:build windows

package app

import (
	"os/exec"

	"cursor-inner/internal/winproc"
)

func folderCommand(dir string) *exec.Cmd { return winproc.GUI("explorer", dir) }

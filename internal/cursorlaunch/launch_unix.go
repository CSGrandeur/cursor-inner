//go:build darwin || linux

package cursorlaunch

import (
	"os/exec"
	"runtime"
)

func Start() error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-a", "Cursor").Start()
	}
	path, err := findCursor(nil, func() (string, error) { return exec.LookPath("cursor") })
	if err != nil {
		return err
	}
	return exec.Command(path).Start()
}

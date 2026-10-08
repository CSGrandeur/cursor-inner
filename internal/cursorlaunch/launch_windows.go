//go:build windows

package cursorlaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Start() error {
	path, err := findCursor(windowsCandidates(), whereCursor)
	if err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = withoutElectronNode(os.Environ())
	return cmd.Start()
}

func withoutElectronNode(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), "ELECTRON_RUN_AS_NODE=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func windowsCandidates() []string {
	return []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "cursor", "Cursor.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "cursor", "Cursor.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "cursor", "Cursor.exe"),
	}
}

func whereCursor() (string, error) {
	for _, name := range []string{"Cursor.exe", "cursor"} {
		out, err := exec.Command("where.exe", name).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(strings.TrimRight(line, "\r"))
			if exe := cursorExecutable(line); exe != "" {
				return exe, nil
			}
		}
	}
	return "", os.ErrNotExist
}

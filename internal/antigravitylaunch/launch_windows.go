//go:build windows

package antigravitylaunch

import (
	"os"
	"path/filepath"

	"cursor-inner/internal/winproc"
)

// Launch 打开已安装的 Antigravity。已有进程在跑时不再开一份。
// proxyURL 非空时带上与 Apply 相同的代理参数和环境变量（含 Go OAuth 认的 HTTPS_PROXY）。
func Launch(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return err
	}
	if len(procs) > 0 {
		return nil
	}
	path, err := findAntigravity(windowsCandidates(), whereAntigravity)
	if err != nil {
		return err
	}
	return start(path, proxyURL)
}

func windowsCandidates() []string {
	return []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Antigravity", "Antigravity.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "antigravity", "Antigravity.exe"),
	}
}

func whereAntigravity() (string, error) {
	for _, name := range []string{"Antigravity.exe", "antigravity"} {
		for _, line := range winproc.Where(name) {
			if exe := antigravityExecutable(line); exe != "" {
				return exe, nil
			}
		}
	}
	return "", os.ErrNotExist
}

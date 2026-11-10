//go:build darwin || linux

package antigravitylaunch

import (
	"os"
	"os/exec"
	"runtime"
)

// Launch 在非 Windows 上冷启动 Antigravity。proxyURL 暂未注入（接管链路目前以 Windows 为主）。
func Launch(proxyURL string) error {
	_ = proxyURL
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-a", "Antigravity").Start()
	}
	path, err := findAntigravity(nil, func() (string, error) { return exec.LookPath("antigravity") })
	if err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Env = Env(proxyURL, os.Environ())
	return cmd.Start()
}

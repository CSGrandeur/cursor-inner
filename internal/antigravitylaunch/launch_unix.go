//go:build darwin || linux

package antigravitylaunch

import (
	"os"
	"os/exec"
	"runtime"
)

// Launch 在非 Windows 上冷启动 Antigravity；Linux 带上代理参数与进程环境。
func Launch(proxyURL string) error {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("open", "-a", "Antigravity")
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	path, err := findAntigravity(nil, func() (string, error) { return exec.LookPath("antigravity") })
	if err != nil {
		return err
	}
	cmd := exec.Command(path, Args(proxyURL)...)
	cmd.Env = Env(proxyURL, os.Environ())
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

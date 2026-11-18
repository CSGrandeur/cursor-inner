//go:build darwin || linux

package antigravitylaunch

import (
	"os"
	"os/exec"
	"runtime"
)

// Launch 在非 Windows 上冷启动 Antigravity；Linux 与 macOS 均带上代理参数与进程环境。
func Launch(proxyURL string) error {
	if runtime.GOOS == "darwin" {
		cmdArgs := []string{"-a", "Antigravity"}
		if args := Args(proxyURL); len(args) > 0 {
			cmdArgs = append(cmdArgs, "--args")
			cmdArgs = append(cmdArgs, args...)
		}
		cmd := exec.Command("open", cmdArgs...)
		cmd.Env = Env(proxyURL, os.Environ())
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

// installedExe 定位本机已安装的 Antigravity 可执行路径（运行中进程 → PATH）。
func installedExe() string {
	if exe := ExePath(); exe != "" {
		return exe
	}
	if p, err := exec.LookPath("antigravity"); err == nil {
		return p
	}
	return ""
}

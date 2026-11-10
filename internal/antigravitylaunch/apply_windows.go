//go:build windows

package antigravitylaunch

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/winproc"
)

const exeName = "Antigravity.exe"

type process struct {
	ExecutablePath string
	CommandLine    string
}

var applyMu sync.Mutex

var (
	listAnti  = running
	stopAnti  = stop
	startAnti = start
)

// Apply 让正在运行的 Antigravity 改走 proxyURL（Chromium 参数 + HTTPS_PROXY）。
// proxyURL 为空时去掉我们加上的参数并重新打开。没在运行就不动。不改系统 hosts / PAC。
func Apply(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listAnti()
	if err != nil {
		return err
	}
	if len(procs) == 0 {
		return nil
	}
	exe := ""
	for _, p := range procs {
		if p.ExecutablePath != "" {
			exe = p.ExecutablePath
			break
		}
	}
	if exe == "" {
		return nil
	}
	if proxyURL == "" {
		if !grokbot.ShouldRestart(cmds(procs), "") {
			_ = RestoreLanguageServer(exe)
			return nil
		}
		if err := stopAll(); err != nil {
			return err
		}
		_ = RestoreLanguageServer(exe)
		slog.Info("已去掉 Antigravity 的代理参数并重新打开")
		return startAnti(exe, "")
	}
	needRestart := grokbot.ShouldRestart(cmds(procs), proxyURL) || !LanguageServerProxyReady(exe, proxyURL)
	if !needRestart {
		return nil
	}
	if err := stopAll(); err != nil {
		return err
	}
	if err := EnsureLanguageServerProxy(exe, proxyURL); err != nil {
		slog.Warn("Antigravity language_server 代理包装失败：" + err.Error())
	}
	if err := startAnti(exe, proxyURL); err != nil {
		slog.Warn("没能重新打开 Antigravity：" + err.Error())
		return err
	}
	slog.Info("Antigravity 已改走配置的代理 " + proxyURL)
	return nil
}

func stopAll() error {
	err := stopAnti()
	if err == nil {
		return nil
	}
	left, lerr := listAnti()
	if lerr == nil && len(left) == 0 {
		slog.Warn("结束 Antigravity 时报错，但进程已全部退出，继续重新打开：" + err.Error())
		return nil
	}
	slog.Warn("没能结束 Antigravity，保持现状：" + err.Error())
	return err
}

func cmds(procs []process) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.CommandLine)
	}
	return out
}

func running() ([]process, error) {
	procs, err := winproc.List(exeName)
	if err != nil {
		return nil, err
	}
	var out []process
	for _, p := range procs {
		if p.Exe == "" && p.CommandLine == "" {
			continue
		}
		out = append(out, process{ExecutablePath: p.Exe, CommandLine: p.CommandLine})
	}
	return out, nil
}

func stop() error {
	procs, err := winproc.List(exeName)
	if err != nil {
		return err
	}
	pids := make([]uint32, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	return winproc.KillTree(pids...)
}

func start(exe, proxyURL string) error {
	if proxyURL == "" {
		_ = RestoreLanguageServer(exe)
	} else if err := EnsureLanguageServerProxy(exe, proxyURL); err != nil {
		slog.Warn("Antigravity language_server 代理包装失败：" + err.Error())
	}
	cmd := winproc.GUI(exe, Args(proxyURL)...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = Env(proxyURL, os.Environ())
	return cmd.Start()
}

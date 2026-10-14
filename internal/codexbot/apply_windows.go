//go:build windows

package codexbot

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/winproc"
)

type process struct {
	ProcessID      uint32
	ExecutablePath string
	CommandLine    string
}

var applyMu sync.Mutex

// Apply 让正在运行的 Codex 桌面版改走 proxyURL。proxyURL 为空时去掉我们加上的参数并重新打开。
// 没在运行就不动。不改系统 hosts，不写用户级环境变量。终端里单独启动的 codex CLI 不在此列。
func Apply(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return err
	}
	shells := desktopShells(procs)
	if len(shells) == 0 {
		return nil
	}
	exe := shells[0].ExecutablePath
	if proxyURL == "" {
		if !grokbot.ShouldRestart(cmds(shells), "") {
			return nil
		}
		if err := stop(shells); err != nil {
			return err
		}
		slog.Info("已去掉 Codex 的代理参数并重新打开")
		return start(exe, "")
	}
	if !grokbot.ShouldRestart(cmds(shells), proxyURL) {
		return nil
	}
	if err := stop(shells); err != nil {
		return err
	}
	if err := start(exe, proxyURL); err != nil {
		slog.Warn("没能重新打开 Codex：" + err.Error())
		return err
	}
	slog.Info("Codex 已改走配置的代理")
	return nil
}

func cmds(procs []process) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.CommandLine)
	}
	return out
}

func desktopShells(procs []process) []process {
	out := make([]process, 0, len(procs))
	for _, p := range procs {
		if isDesktopShell(p.ExecutablePath, p.CommandLine) {
			out = append(out, p)
		}
	}
	return out
}

func running() ([]process, error) {
	procs, err := winproc.List("Codex.exe", "ChatGPT.exe")
	if err != nil {
		return nil, err
	}
	var out []process
	for _, p := range procs {
		if p.Exe == "" && p.CommandLine == "" {
			continue
		}
		out = append(out, process{ProcessID: p.PID, ExecutablePath: p.Exe, CommandLine: p.CommandLine})
	}
	return out, nil
}

func stop(shells []process) error {
	pids := make([]uint32, 0, len(shells))
	for _, p := range shells {
		if p.ProcessID > 0 {
			pids = append(pids, p.ProcessID)
		}
	}
	return winproc.KillTree(pids...)
}

func start(exe, proxyURL string) error {
	cmd := winproc.GUI(exe, Args(proxyURL)...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = Env(proxyURL, os.Environ())
	return cmd.Start()
}

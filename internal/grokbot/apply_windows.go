//go:build windows

package grokbot

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"cursor-inner/internal/winproc"
)

const exeName = "Grok Bot.exe"

type process struct {
	ExecutablePath string
	CommandLine    string
}

var applyMu sync.Mutex

// 测试替换用。生产路径就是下面的 running / stop / start。
var (
	listGrok  = running
	stopGrok  = stop
	startGrok = start
)

// Apply 让正在运行的 Grok Bot 改走 proxyURL。proxyURL 为空时，去掉我们加上的参数并重新打开。
// 没在运行就不动。不改系统 hosts。
func Apply(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listGrok()
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
		if !ShouldRestart(cmds(procs), "") {
			return nil
		}
		if err := stopAll(); err != nil {
			return err
		}
		slog.Info("已去掉 Grok Bot 的代理参数并重新打开")
		return startGrok(exe, "")
	}
	if !ShouldRestart(cmds(procs), proxyURL) {
		return nil
	}
	if err := stopAll(); err != nil {
		return err
	}
	if err := startGrok(exe, proxyURL); err != nil {
		slog.Warn("没能重新打开 Grok Bot：" + err.Error())
		return err
	}
	slog.Info("Grok Bot 已改走配置的代理 " + proxyURL)
	return nil
}

// stopAll 结束全部 Grok Bot 进程。结束时报错但进程其实都已退出，就不算失败：
// 否则 Grok 被关掉却不再重新打开，用户看到的就是窗口闪一下没了，日志里也什么都没有。
func stopAll() error {
	err := stopGrok()
	if err == nil {
		return nil
	}
	left, lerr := listGrok()
	if lerr == nil && len(left) == 0 {
		slog.Warn("结束 Grok Bot 时报错，但进程已全部退出，继续重新打开：" + err.Error())
		return nil
	}
	slog.Warn("没能结束 Grok Bot，保持现状：" + err.Error())
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
	cmd := exec.Command(exe, ProxyArgs(proxyURL)...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = ProxyEnv(proxyURL, os.Environ())
	return cmd.Start()
}

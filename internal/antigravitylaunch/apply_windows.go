//go:build windows

package antigravitylaunch

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/winproc"
)

const exeName = "Antigravity.exe"

type process struct {
	ExecutablePath string
	CommandLine    string
}

var (
	applyMu   sync.Mutex
	lastState struct {
		set         bool
		running     bool
		wantProxy   bool
		needRestart bool
	}
)

var (
	listAnti  = running
	stopAnti  = stop
	startAnti = start
)

func logCheckState(running, wantProxy, needRestart bool) {
	if lastState.set && lastState.running == running && lastState.wantProxy == wantProxy && lastState.needRestart == needRestart {
		return
	}
	lastState.set = true
	lastState.running = running
	lastState.wantProxy = wantProxy
	lastState.needRestart = needRestart
	slog.Debug("antigravity takeover check", "running", running, "want_proxy", wantProxy, "need_restart", needRestart)
}

// Apply 让正在运行的 Antigravity 改走 proxyURL（Chromium 参数 + 进程内 HTTPS_PROXY/NO_PROXY/WSLENV），
// 仅在 Antigravity.exe 进程环境块内无感覆盖 Electron、Go OAuth 与其拉起的 WSL language_server；
// proxyURL 为空时去掉我们加上的参数并重新打开。没在运行就不动。不改系统 hosts / PAC / 全局环境变量，也不改 WSL 内任何文件。
func Apply(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listAnti()
	if err != nil {
		return err
	}
	if len(procs) == 0 {
		logCheckState(false, proxyURL != "", false)
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
	// 无条件自愈：若磁盘仍残留早期版本的 language_server.exe 包装，不重启宿主直接还原。
	if rerr := RestoreLanguageServer(exe); rerr != nil {
		slog.Debug("restore antigravity language_server skipped", "error", rerr.Error())
	}
	needRestart := grokbot.ShouldRestart(cmds(procs), proxyURL)
	logCheckState(true, proxyURL != "", needRestart)
	if !needRestart {
		return nil
	}
	if proxyURL == "" {
		if err := stopAll(); err != nil {
			return err
		}
		slog.Info("已去掉 Antigravity 的代理参数并重新打开")
		return startAnti(exe, "")
	}
	if err := stopAll(); err != nil {
		return err
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
	// 只逐个结束 Antigravity.exe，严禁 KillTree（WSL 模式下会误杀 wsl.exe 并拖挂 WSLService）。
	var firstErr error
	for _, p := range procs {
		if err := winproc.Kill(p.PID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func start(exe, proxyURL string) error {
	_ = RestoreLanguageServer(exe)
	cmd := winproc.GUI(exe, Args(proxyURL)...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = Env(proxyURL, os.Environ())
	if err := cmd.Start(); err != nil {
		slog.Warn("启动 Antigravity 进程失败", "exe", exe, "error", err.Error())
		return err
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	slog.Info("已拉起 Antigravity 进程", "exe", exe, "pid", pid, logx.FileOnly)
	return nil
}

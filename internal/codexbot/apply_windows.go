//go:build windows

package codexbot

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"cursor-inner/internal/grokbot"
)

type process struct {
	ProcessID      int    `json:"ProcessId"`
	ExecutablePath string `json:"ExecutablePath"`
	CommandLine    string `json:"CommandLine"`
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
	script := `Get-CimInstance Win32_Process -Filter "Name = 'Codex.exe' OR Name = 'ChatGPT.exe'" | Select-Object ProcessId, ExecutablePath, CommandLine | ConvertTo-Json -Compress`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(out))
	if text == "" || text == "null" {
		return nil, nil
	}
	if strings.HasPrefix(text, "[") {
		var procs []process
		if err := json.Unmarshal([]byte(text), &procs); err != nil {
			return nil, err
		}
		return procs, nil
	}
	var one process
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		return nil, err
	}
	if one.ExecutablePath == "" && one.CommandLine == "" {
		return nil, nil
	}
	return []process{one}, nil
}

func stop(shells []process) error {
	for _, p := range shells {
		if p.ProcessID <= 0 {
			continue
		}
		out, err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.ProcessID)).CombinedOutput()
		if err == nil {
			continue
		}
		text := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(text), "not found") || strings.Contains(text, "没有找到") || strings.Contains(text, "找不到") {
			continue
		}
		if text == "" {
			return err
		}
		return err
	}
	return nil
}

func start(exe, proxyURL string) error {
	cmd := exec.Command(exe, Args(proxyURL)...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = Env(proxyURL, os.Environ())
	return cmd.Start()
}

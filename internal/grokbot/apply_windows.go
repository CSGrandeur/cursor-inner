//go:build windows

package grokbot

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type process struct {
	ExecutablePath string
	CommandLine    string
}

var applyMu sync.Mutex

// Apply 让正在运行的 Grok Bot 改走 proxyURL。proxyURL 为空时，去掉我们加上的参数并重新打开。
// 没在运行就不动。不改系统 hosts。
func Apply(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
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
		if err := stop(); err != nil {
			return err
		}
		slog.Info("已去掉 Grok Bot 的代理参数并重新打开")
		return start(exe, "")
	}
	if !ShouldRestart(cmds(procs), proxyURL) {
		return nil
	}
	if err := stop(); err != nil {
		return err
	}
	if err := start(exe, proxyURL); err != nil {
		slog.Warn("没能重新打开 Grok Bot：" + err.Error())
		return err
	}
	slog.Info("Grok Bot 已改走配置的代理 " + proxyURL)
	return nil
}

func cmds(procs []process) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.CommandLine)
	}
	return out
}

func running() ([]process, error) {
	script := `Get-CimInstance Win32_Process -Filter "Name = 'Grok Bot.exe'" | Select-Object ExecutablePath, CommandLine | ConvertTo-Json -Compress`
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

func stop() error {
	out, err := exec.Command("taskkill", "/F", "/T", "/IM", "Grok Bot.exe").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(text), "not found") || strings.Contains(text, "没有找到") || strings.Contains(text, "找不到") {
		return nil
	}
	if text == "" {
		return err
	}
	return err
}

func start(exe, proxyURL string) error {
	var args []string
	if proxyURL != "" {
		args = []string{"--proxy-server=" + proxyURL, "--disable-quic"}
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = envFor(proxyURL)
	return cmd.Start()
}

func envFor(proxyURL string) []string {
	out := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "ELECTRON_RUN_AS_NODE=") {
			continue
		}
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		out = append(out, entry)
	}
	if proxyURL == "" {
		return out
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		out = append(out, key+"="+proxyURL)
	}
	out = append(out, "NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1")
	return out
}

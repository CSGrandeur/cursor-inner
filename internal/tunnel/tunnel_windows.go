//go:build windows

package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func (m *Manager) scriptPath() string { return filepath.Join(m.dir, "tun-egress.ps1") }

func (m *Manager) writeScript() error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.scriptPath(), []byte(Script), 0o600)
}

func startTunnel(m *Manager) error { return runScript(m, "start") }
func stopTunnel(m *Manager) error  { return runScript(m, "stop") }

// runScript 把脚本落盘，经 Start-Process -Verb RunAs（触发 UAC）以管理员身份运行，并把退出码透传回来。
func runScript(m *Manager, action string) error {
	if err := m.writeScript(); err != nil {
		return err
	}
	inner := []string{
		"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", m.scriptPath(),
		"-Action", action,
		"-ConfigPath", m.ConfigPath(),
		"-BinaryPath", m.BinaryPath(),
		"-LogPath", m.LogPath(),
		"-PidPath", m.pidPath(),
	}
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
		fmt.Sprintf("$p=Start-Process powershell -Verb RunAs -PassThru -Wait -ArgumentList %s; exit $p.ExitCode", psArgList(inner)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return i18n.E("TUN "+action+" 失败："+string(out), "TUN "+action+" failed: "+string(out))
	}
	return nil
}

// psArgList 把参数拼成 PowerShell 数组字面量 @('a','b')，单引号内按 PowerShell 规则转义。
func psArgList(args []string) string {
	q := make([]string, 0, len(args))
	for _, a := range args {
		q = append(q, "'"+strings.ReplaceAll(a, "'", "''")+"'")
	}
	return "@(" + strings.Join(q, ",") + ")"
}

// DetectDNS 读取当前所有网卡的 IPv4 DNS 服务器，供生成配置时作为“真实 DNS 上游”。
func DetectDNS() []string {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-DnsClientServerAddress -AddressFamily IPv4).ServerAddresses | Sort-Object -Unique")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var res []string
	for _, line := range strings.Split(string(out), "\n") {
		s := strings.TrimSpace(line)
		if s != "" {
			res = append(res, s)
		}
	}
	return res
}

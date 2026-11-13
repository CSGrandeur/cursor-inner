//go:build windows

package tunnel

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"cursor-inner/internal/i18n"
	"cursor-inner/internal/winproc"
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
	cmd := winproc.Quiet("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
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

// DetectDNS 通过进程内 GetAdaptersAddresses 读取当前启用网卡的 IPv4 DNS 服务器，供生成配置时作为“真实 DNS 上游”。
// 不起 PowerShell 子进程，不弹黑框。
func DetectDNS() []string {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(15000)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW || size == 0 {
			return nil
		}
	}
	seen := map[string]bool{}
	var res []string
	aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
	for ; aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		desc := strings.ToLower(windows.UTF16PtrToString(aa.Description))
		name := strings.ToLower(windows.UTF16PtrToString(aa.FriendlyName))
		if strings.Contains(desc, "sing-box") || strings.Contains(name, "sing-box") {
			continue
		}
		for dns := aa.FirstDnsServerAddress; dns != nil; dns = dns.Next {
			if dns.Address.Sockaddr == nil {
				continue
			}
			sa, err := dns.Address.Sockaddr.Sockaddr()
			if err != nil {
				continue
			}
			v4, ok := sa.(*syscall.SockaddrInet4)
			if !ok {
				continue
			}
			ip := net.IP(v4.Addr[:])
			if ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			s := ip.String()
			if !seen[s] {
				seen[s] = true
				res = append(res, s)
			}
		}
	}
	sort.Strings(res)
	return res
}

func detectInitialState(m *Manager) State {
	if m == nil || strings.TrimSpace(m.dir) == "" {
		return StateOff
	}
	var pid uint32
	if b, err := os.ReadFile(m.pidPath()); err == nil {
		if n, perr := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32); perr == nil {
			pid = uint32(n)
		}
	}
	procs, err := winproc.List("sing-box.exe")
	if err != nil {
		return StateOff
	}
	bin := m.BinaryPath()
	for _, p := range procs {
		if (pid > 0 && p.PID == pid) || (p.Exe != "" && strings.EqualFold(p.Exe, bin)) {
			return StateOn
		}
	}
	return StateOff
}

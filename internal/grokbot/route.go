package grokbot

import (
	"net"
	"net/url"
	"strings"
)

// HTTPProxyURL 把配置里的代理地址变成 Grok Bot 能用的 HTTP 代理。
// 它的官方客户端读 HTTPS_PROXY，按 HTTP CONNECT 去连，不认 socks 握手。
// 只取主机和端口，协议统一写成 http。
func HTTPProxyURL(spec string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", false
	}
	if !strings.Contains(spec, "://") {
		spec = "http://" + spec
	}
	u, err := url.Parse(spec)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", false
	}
	return "http://" + net.JoinHostPort(u.Hostname(), u.Port()), true
}

// proxyConflict 报告命令行是否带有会拆掉固定代理的 Chromium 开关。
// --no-proxy-server 会覆盖 --proxy-server；PAC / 自动检测会回到系统 PAC（v2rayN PAC 模式下部分域名 DIRECT）。
func proxyConflict(cmdline string) bool {
	lower := strings.ToLower(cmdline)
	for _, bad := range []string{
		"--no-proxy-server",
		"--proxy-auto-detect",
		"--proxy-pac-url=",
	} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	// --proxy-server=...direct:// 或 "http://p,direct://" 会在代理失败时直连。
	if i := strings.Index(lower, "--proxy-server="); i >= 0 {
		rest := lower[i+len("--proxy-server="):]
		end := strings.IndexAny(rest, " \t\"'")
		if end >= 0 {
			rest = rest[:end]
		}
		if strings.Contains(rest, "direct://") || strings.Contains(rest, ",direct") {
			return true
		}
	}
	return false
}

// Routed 报告这条命令行是不是已经带上我们加上的代理参数，且没有冲突开关。
// 参数按完整 token 比对，避免回环 --proxy-bypass-list 被旧版带额外条目的更长名单子串误匹配。
func Routed(cmdline, proxyURL string) bool {
	if proxyURL == "" || proxyConflict(cmdline) {
		return false
	}
	for _, arg := range ProxyArgs(proxyURL) {
		if !hasArg(cmdline, arg) {
			return false
		}
	}
	return true
}

func hasArg(cmdline, arg string) bool {
	for idx := 0; idx <= len(cmdline); {
		i := strings.Index(cmdline[idx:], arg)
		if i < 0 {
			return false
		}
		i += idx
		end := i + len(arg)
		if i > 0 {
			switch cmdline[i-1] {
			case ' ', '\t', '"', '\'':
			default:
				idx = i + 1
				continue
			}
		}
		if end < len(cmdline) {
			switch cmdline[end] {
			case ' ', '\t', '"', '\'':
			default:
				idx = i + 1
				continue
			}
		}
		return true
	}
	return false
}

// ShouldRestart 判断正在运行的 Grok Bot 要不要关掉再开。
// 已经有一个进程带上当前代理时不要关：登录回跳会再拉起一个没有这些参数的进程，
// 关掉正在轮询登录结果的那一个，网页上的登录就对不上了。
// 命令行还没读到的进程忽略。proxyURL 为空时，只有带了我们参数的进程才需要清掉重开。
func ShouldRestart(cmds []string, proxyURL string) bool {
	if proxyURL == "" {
		for _, cmd := range cmds {
			if strings.Contains(cmd, "--proxy-server=") && strings.Contains(cmd, "--disable-quic") {
				return true
			}
			if strings.Contains(cmd, EnvProxyMark) {
				return true
			}
		}
		return false
	}
	unrouted := false
	for _, cmd := range cmds {
		if cmd == "" || strings.Contains(cmd, "--type=") {
			continue
		}
		if Routed(cmd, proxyURL) {
			return false
		}
		unrouted = true
	}
	return unrouted
}

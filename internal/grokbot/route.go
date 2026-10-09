package grokbot

import (
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
	return "http://" + u.Hostname() + ":" + u.Port(), true
}

// Routed 报告这条命令行是不是已经带上我们加上的代理参数。
func Routed(cmdline, proxyURL string) bool {
	if proxyURL == "" {
		return false
	}
	for _, arg := range ProxyArgs(proxyURL) {
		if !strings.Contains(cmdline, arg) {
			return false
		}
	}
	return true
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

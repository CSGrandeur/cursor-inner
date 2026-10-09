package grokbot

import "strings"

// EnvProxyMark 出现在命令行里，表示这次启动已带上 undici 认的环境代理开关。
// 旧进程只有 --proxy-server 和 --disable-quic 时 Routed 为假，ShouldRestart 会重开。
const EnvProxyMark = "--use-env-proxy"

// ProxyArgs 返回接管 Grok 时要加的启动参数。proxyURL 为空时返回 nil。
func ProxyArgs(proxyURL string) []string {
	if proxyURL == "" {
		return nil
	}
	return []string{"--proxy-server=" + proxyURL, "--disable-quic", EnvProxyMark}
}

// ProxyEnv 在 base 环境上写好 Grok Bot 走 HTTP 代理所需的变量。
// Electron 窗口认 --proxy-server；主进程 undici 要 HTTPS_PROXY，且默认不读，
// 除非 NODE_USE_ENV_PROXY=1（或等价的 --use-env-proxy）。
func ProxyEnv(proxyURL string, base []string) []string {
	out := make([]string, 0, len(base)+10)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "ELECTRON_RUN_AS_NODE",
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
			"NODE_USE_ENV_PROXY":
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
	out = append(out,
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
		"NODE_USE_ENV_PROXY=1",
	)
	return out
}

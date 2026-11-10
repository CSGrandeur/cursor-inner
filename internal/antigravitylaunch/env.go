package antigravitylaunch

import (
	"strings"

	"cursor-inner/internal/grokbot"
)

const lsProxyEnv = "CURSOR_INNER_LS_PROXY"

// Args 与 Grok/Codex 同一套 Chromium 代理启动参数：固定 --proxy-server，关 QUIC，
// 绕过名单只有回环和局域网。窗口流量认这个参数，不认系统 PAC。
func Args(proxyURL string) []string {
	return grokbot.ProxyArgs(proxyURL)
}

// Env 给 Electron 主进程和其子进程（含 Go OAuth / language_server）写 HTTPS_PROXY 等。
// oauth2.googleapis.com 的 dial 走进程环境代理，才能进入 cursor-inner 接管链路。
// 另写 CURSOR_INNER_LS_PROXY，供 language_server 包装进程读取（防 shellEnvSync 冲掉）。
// 同时清掉 ELECTRON_RUN_AS_NODE，避免被当成 node 启动。
func Env(proxyURL string, base []string) []string {
	out := make([]string, 0, len(base)+8)
	for _, entry := range grokbot.ProxyEnv(proxyURL, base) {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "ELECTRON_RUN_AS_NODE") || strings.EqualFold(name, lsProxyEnv) {
			continue
		}
		out = append(out, entry)
	}
	if strings.TrimSpace(proxyURL) != "" {
		out = append(out, lsProxyEnv+"="+proxyURL)
	}
	return out
}

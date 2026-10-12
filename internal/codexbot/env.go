package codexbot

import (
	"strings"

	"cursor-inner/internal/grokbot"
)

// Args 是写进 Codex 窗口进程的 Chromium 参数，和 Grok Bot 同一套：
// 固定 --proxy-server，关 QUIC，绕过名单只有回环和局域网。
// 窗口流量只认这个参数；系统代理、用户环境变量和 ~/.codex/.env 到不了渲染进程。
func Args(proxyURL string) []string {
	return grokbot.ProxyArgs(proxyURL)
}

// Env 在窗口进程上写代理变量。app-server（reqwest）按 HTTPS_PROXY、HTTP_PROXY、ALL_PROXY 的顺序走代理，
// WebSocket 还要进程里的 WS_PROXY / ALL_PROXY。不继承原来的 NO_PROXY，也不写用户级环境变量。
func Env(proxyURL string, base []string) []string {
	out := make([]string, 0, len(base)+8)
	for _, entry := range grokbot.ProxyEnv(proxyURL, base) {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "WS_PROXY", "WSS_PROXY":
			continue
		}
		out = append(out, entry)
	}
	if proxyURL == "" {
		return out
	}
	for _, key := range []string{"WS_PROXY", "WSS_PROXY", "ws_proxy", "wss_proxy"} {
		out = append(out, key+"="+proxyURL)
	}
	return out
}

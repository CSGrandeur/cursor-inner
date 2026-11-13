package antigravitylaunch

import (
	"strings"

	"cursor-inner/internal/grokbot"
)

// Args 与 Grok/Codex 同一套 Chromium 代理启动参数：固定 --proxy-server，关 QUIC，
// 绕过名单只有回环和局域网。窗口流量认这个参数，不认系统 PAC。
func Args(proxyURL string) []string {
	return grokbot.ProxyArgs(proxyURL)
}

// wslProxyEnvNames 是经单进程私有 WSLENV 透传给 Antigravity 所拉起 WSL 子进程的代理变量。
// Windows PEB 大小写去重后仅保留在后写入的大写键，因此 WSLENV 只写规范大写名（不写小写，否则 wsl.exe 会导出空值变量），
// 不加 /p 或 /l，避免路径翻译改坏 http://127.0.0.1:端口；包含 NO_PROXY 使回环和私网直连，
// 包含 NODE_USE_ENV_PROXY 让 Node 子进程同样遵循。绝不含 CURSOR_INNER_LS_PROXY。
var wslProxyEnvNames = []string{
	"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
	"GRPC_PROXY", "NODE_USE_ENV_PROXY",
}

func isManagedWSLProxyVar(name string) bool {
	if strings.EqualFold(name, "CURSOR_INNER_LS_PROXY") {
		return true
	}
	for _, n := range wslProxyEnvNames {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// MergeWSLENV 在保留既有自定义条目的前提下，管理传给 WSL 子进程的代理变量列表（不加 /p 或 /l）。
// proxyURL 为空时剔除受管代理项及历史 CURSOR_INNER_LS_PROXY；非空时清理旧项后追加全部 wslProxyEnvNames。
func MergeWSLENV(existing, proxyURL string) string {
	var parts []string
	for _, raw := range strings.Split(existing, ":") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		name, _, _ := strings.Cut(p, "/")
		if isManagedWSLProxyVar(strings.TrimSpace(name)) {
			continue
		}
		parts = append(parts, p)
	}
	if strings.TrimSpace(proxyURL) != "" {
		parts = append(parts, wslProxyEnvNames...)
	}
	return strings.Join(parts, ":")
}

func withWSLENV(env []string, proxyURL string) []string {
	existing := ""
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "WSLENV") {
			existing = value
			continue
		}
		out = append(out, entry)
	}
	if merged := MergeWSLENV(existing, proxyURL); merged != "" {
		out = append(out, "WSLENV="+merged)
	}
	return out
}

// Env 给 Electron 主进程和其子进程（含 Go OAuth 与其拉起的 WSL language_server）写 HTTPS_PROXY、
// NO_PROXY 等，并在单进程私有环境块内通过 WSLENV 无感透传给 WSL 子进程；
// 清掉 ELECTRON_RUN_AS_NODE 与历史 CURSOR_INNER_LS_PROXY，绝不改动系统环境变量或 WSL 文件。
func Env(proxyURL string, base []string) []string {
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "ELECTRON_RUN_AS_NODE") || strings.EqualFold(name, "CURSOR_INNER_LS_PROXY") {
			continue
		}
		filtered = append(filtered, entry)
	}
	proxied := grokbot.ProxyEnv(proxyURL, filtered)
	return withWSLENV(proxied, proxyURL)
}

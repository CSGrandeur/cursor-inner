package app

import (
	"log/slog"
	"net/url"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/i18n"
)

// grokProxy 算出给 Grok Bot 的代理地址。
//   - keep 为真：现在没法给出可靠地址（地址填错、桥起不来），保持 Grok Bot 现状，绝不改成直连。
//   - proxyURL 为空且 keep 为假：用户关掉了代理，按用户选择不带代理。
//
// 地址带认证或是 socks 时，Grok Bot 自己用不了，改由本机回环上的代理桥转发。
func (a *App) grokProxy(cfg config.File) (proxyURL string, keep bool) {
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil {
		return "", true
	}
	if !on {
		return "", false
	}
	if grokbot.NeedsBridge(spec) {
		d, err := dialer.FromProxy(cfg.Proxy)
		if err != nil {
			slog.Warn("Grok 代理桥没法用当前代理：" + err.Error())
			return "", true
		}
		u, err := a.bridge.Ensure(spec, grokbot.DialFunc(d))
		if err != nil {
			slog.Warn("Grok 代理桥没能监听：" + err.Error())
			return "", true
		}
		return u, false
	}
	u, ok := grokbot.HTTPProxyURL(spec)
	if !ok {
		return "", true
	}
	return u, false
}

// SyncGrok 按「接管 Grok」开关处理正在运行的 Grok Bot。代理写在启动参数里，正在运行的进程读不到，
// 所以状态不对时会关掉并按当前选择重新打开。没在运行不拉起。不改系统 hosts。
func (a *App) SyncGrok() {
	if a.frozen.Load() {
		return
	}
	cfg := a.store.Get()
	if !cfg.TakeoverGrok {
		_ = grokbot.Apply("")
		return
	}
	proxyURL, keep := a.grokProxy(cfg)
	if keep {
		return
	}
	_ = grokbot.Apply(proxyURL)
}

// OpenGrok 冷启动 Grok Bot。接管开着却给不出可靠代理时报错，不以直连方式启动。
func (a *App) OpenGrok() error {
	cfg := a.store.Get()
	if !cfg.TakeoverGrok {
		slog.Info("启动 Grok Bot", "takeover", false)
		return grokbot.Launch("")
	}
	proxyURL, keep := a.grokProxy(cfg)
	if keep {
		slog.Warn("没有启动 Grok Bot：拿不到可靠代理，不会改成直连")
		return i18n.E("代理地址无效或代理桥起不来，没有启动 Grok（不会以直连方式启动）", "The proxy address is invalid or the proxy bridge could not start, so Grok was not started (it is never started without the proxy)")
	}
	slog.Info("启动 Grok Bot", "takeover", true, "proxy", proxyHostOnly(proxyURL))
	return grokbot.Launch(proxyURL)
}

// proxyHostOnly 只保留代理的协议和主机，丢掉可能带着的账号口令，避免写进日志。
func proxyHostOnly(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "set"
}

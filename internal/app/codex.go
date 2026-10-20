package app

import (
	"log/slog"

	"cursor-inner/internal/codexbot"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/i18n"
)

// codexProxy 算出给 Codex 桌面版的代理地址。这个进程的联网全部走代理，不按“仅官方”拆开：
// OpenAI 的地址不在官方名单里，拆开就会直连。
//   - keep 为真：现在给不出可靠地址，保持现状，绝不改成直连。
//   - proxyURL 为空且 keep 为假：用户关掉了代理，按用户选择不带代理。
//
// socks 或带账号口令时，Codex 自己用不了，改走本机回环上的代理桥。桥只为 Codex 服务，出站始终是配置的代理。
func (a *App) codexProxy(cfg config.File) (proxyURL string, keep bool) {
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil {
		return "", true
	}
	if !on {
		a.codexBridge.Close()
		return "", false
	}
	// 不带协议时先探测是 socks5 还是 http，再决定 Grok/Codex 能否直接用。
	spec = dialer.Resolve(spec)
	if grokbot.NeedsBridge(spec) {
		d, err := dialer.FromProxy(cfg.Proxy)
		if err != nil {
			slog.Warn("Codex 代理桥没法用当前代理：" + err.Error())
			return "", true
		}
		u, err := a.codexBridge.Ensure("codex\n"+spec, grokbot.DialFunc(d))
		if err != nil {
			slog.Warn("Codex 代理桥没能监听：" + err.Error())
			return "", true
		}
		return u, false
	}
	a.codexBridge.Close()
	u, ok := grokbot.HTTPProxyURL(spec)
	if !ok {
		return "", true
	}
	return u, false
}

// SyncCodex 按“接管 Codex”开关处理正在运行的 Codex 桌面版。
// 代理写在启动参数和这个进程的环境变量里，正在运行的进程读不到，所以状态不对时会关掉并重新打开。
// 没在运行不拉起。
func (a *App) SyncCodex() {
	if a.frozen.Load() {
		return
	}
	cfg := a.store.Get()
	if a.isolated.Load() {
		if cfg.TakeoverCodex {
			if proxyURL, keep := a.codexProxy(cfg); !keep {
				slog.Info("调试实例：不改动 Codex", "codex_proxy", proxyHostOnly(proxyURL))
			}
		}
		return
	}
	if !cfg.TakeoverCodex {
		if !a.ownsMark("codex.managed") {
			return
		}
		if codexApply("") == nil {
			a.dropMark("codex.managed")
		}
		return
	}
	proxyURL, keep := a.codexProxy(cfg)
	if keep {
		return
	}
	if codexApply(proxyURL) == nil {
		a.keepMark("codex.managed", proxyURL)
	}
}

// codexApply 方便测试替换。生产路径就是 codexbot.Apply。
var codexApply = codexbot.Apply

// OpenCodex 冷启动 Codex。接管开着却给不出可靠代理时报错，不以直连方式启动。
func (a *App) OpenCodex() error {
	cfg := a.store.Get()
	if !cfg.TakeoverCodex {
		slog.Info("启动 Codex", "takeover", false)
		return codexbot.Launch("")
	}
	proxyURL, keep := a.codexProxy(cfg)
	if keep {
		slog.Warn("没有启动 Codex：拿不到可靠代理，不会改成直连")
		return i18n.E("代理地址无效或代理桥起不来，没有启动 Codex（不会以直连方式启动）", "The proxy address is invalid or the proxy bridge could not start, so Codex was not started (it is never started without the proxy)")
	}
	slog.Info("启动 Codex", "takeover", true, "proxy", proxyHostOnly(proxyURL))
	return codexbot.Launch(proxyURL)
}

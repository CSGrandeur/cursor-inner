package app

import (
	"log/slog"

	"cursor-inner/internal/antigravitylaunch"
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/i18n"
)

// antiApply 方便测试替换。生产路径就是 antigravitylaunch.Apply。
var antiApply = antigravitylaunch.Apply

// antigravityProxy 算出给 Antigravity 的代理地址。域名已在 official.Suffixes，
// 行为对齐 Grok：仅官方时走本机代理桥分流；socks/带认证也走桥。
// 反重力用独立桥（key 前缀是 antigravity 加换行），日志里 bridge=antigravity，
// 不与 Grok/Cursor 的 bridge=grok 混在一起。
//   - keep 为真：给不出可靠地址，保持现状，绝不改成直连。
//   - proxyURL 为空且 keep 为假：用户关掉了代理，按用户选择不带代理。
func (a *App) antigravityProxy(cfg config.File) (proxyURL string, keep bool) {
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil {
		return "", true
	}
	if !on {
		a.antiBridge.Close()
		return "", false
	}
	if !a.proxyReady() {
		return "", true
	}
	spec = dialer.Resolve(spec)
	if cfg.Proxy.OfficialOnly() || grokbot.NeedsBridge(spec) {
		d, err := dialer.Scoped(cfg.Proxy)
		if err != nil {
			slog.Warn("Antigravity 代理桥没法用当前代理：" + err.Error())
			return "", true
		}
		u, err := a.antiBridge.Ensure("antigravity\n"+spec, grokbot.DialFunc(d))
		if err != nil {
			slog.Warn("Antigravity 代理桥没能监听：" + err.Error())
			return "", true
		}
		return u, false
	}
	a.antiBridge.Close()
	u, ok := grokbot.HTTPProxyURL(spec)
	if !ok {
		return "", true
	}
	return u, false
}

// SyncAntigravity 按“接管 Antigravity”开关处理正在运行的反重力。
// 代理写在启动参数和 HTTPS_PROXY 里（覆盖 Electron 与 Go OAuth），正在运行的进程读不到，
// 所以状态不对时会关掉并按当前选择重新打开。没在运行不拉起。不改系统 PAC/hosts。
func (a *App) SyncAntigravity() {
	if a.frozen.Load() {
		return
	}
	cfg := a.store.Get()
	if a.isolated.Load() {
		if cfg.TakeoverAntigravity {
			if proxyURL, keep := a.antigravityProxy(cfg); !keep {
				slog.Info("调试实例：不改动 Antigravity", "antigravity_proxy", proxyHostOnly(proxyURL), "scope", cfg.Proxy.ScopeName())
			}
		}
		return
	}
	if !cfg.TakeoverAntigravity {
		if !a.ownsMark("antigravity.managed") {
			return
		}
		if antiApply("") == nil {
			a.dropMark("antigravity.managed")
		}
		return
	}
	proxyURL, keep := a.antigravityProxy(cfg)
	if keep {
		return
	}
	if antiApply(proxyURL) == nil {
		a.keepMark("antigravity.managed", proxyURL)
	}
}

// OpenAntigravity 冷启动反重力。接管开着却给不出可靠代理时报错，不以直连方式启动。
func (a *App) OpenAntigravity() error {
	cfg := a.store.Get()
	if !cfg.TakeoverAntigravity {
		slog.Info("启动 Antigravity", "takeover", false)
		return antigravitylaunch.Launch("")
	}
	proxyURL, keep := a.antigravityProxy(cfg)
	if keep && !a.proxyReady() {
		return i18n.E("代理软件还没启动，等它启动后再打开反重力（不会以直连方式启动）", "Your proxy software is not running yet. Start it, then open Antigravity (it is never started without the proxy)")
	}
	if keep {
		slog.Warn("没有启动 Antigravity：拿不到可靠代理，不会改成直连")
		return i18n.E("代理地址无效或代理桥起不来，没有启动反重力（不会以直连方式启动）", "The proxy address is invalid or the proxy bridge could not start, so Antigravity was not started (it is never started without the proxy)")
	}
	slog.Info("启动 Antigravity", "takeover", true, "proxy", proxyHostOnly(proxyURL))
	return antigravitylaunch.Launch(proxyURL)
}

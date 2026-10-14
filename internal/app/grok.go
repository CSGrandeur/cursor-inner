package app

import (
	"cursor-inner/internal/takeover"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/grokbot"
	"cursor-inner/internal/i18n"
)

// grokApply 方便测试替换。生产路径就是 grokbot.Apply。
var grokApply = grokbot.Apply

// grokProxy 算出给 Grok Bot 的代理地址。
//   - keep 为真：现在没法给出可靠地址（地址填错、桥起不来），保持 Grok Bot 现状，绝不改成直连。
//   - proxyURL 为空且 keep 为假：用户关掉了代理，按用户选择不带代理。
//
// 地址带认证或是 socks 时，Grok Bot 自己用不了，改由本机回环上的代理桥转发。
// 代理范围是「仅官方」时也一律经代理桥：桥按目标分流，官方端点走代理，其余直连。
// 桥的端口只由代理地址决定，所以 socks / 带认证的代理切换范围时 Grok Bot 不用重开。
func (a *App) grokProxy(cfg config.File) (proxyURL string, keep bool) {
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil {
		return "", true
	}
	if !on {
		return "", false
	}
	// 不带协议时先探测是 socks5 还是 http，再决定 Grok/Codex 能否直接用。
	spec = dialer.Resolve(spec)
	if cfg.Proxy.OfficialOnly() || grokbot.NeedsBridge(spec) {
		d, err := dialer.Scoped(cfg.Proxy)
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
	if d, err := dialer.FromProxy(cfg.Proxy); err == nil {
		a.bridge.Retarget(grokbot.DialFunc(d))
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
	if a.isolated.Load() {
		// 调试实例不碰正在运行的 Grok Bot（那是用户自己的），只把代理桥按当前设置备好。
		if cfg.TakeoverGrok {
			if proxyURL, keep := a.grokProxy(cfg); !keep {
				slog.Info("调试实例：不改动 Grok Bot", "grok_proxy", proxyHostOnly(proxyURL), "scope", cfg.Proxy.ScopeName())
			}
		}
		return
	}
	if !cfg.TakeoverGrok {
		// 没接管过就什么也不做。另一个数据目录的实例若在这里清参数，会和主实例每 5 秒对拉一次。
		if !a.ownsMark("grok.managed") {
			return
		}
		if grokApply("") == nil {
			a.dropMark("grok.managed")
		}
		return
	}
	proxyURL, keep := a.grokProxy(cfg)
	if keep {
		return
	}
	if grokApply(proxyURL) == nil {
		a.keepMark("grok.managed", proxyURL)
	}
}

func (a *App) ownsMark(name string) bool {
	_, err := os.Stat(filepath.Join(a.store.Dir(), name))
	return err == nil
}

// keepMark 记下接管标记，内容是这次接管用的代理地址（退出时只关带这个代理的进程）。
func (a *App) keepMark(name, proxyURL string) {
	p := filepath.Join(a.store.Dir(), name)
	if old, err := os.ReadFile(p); err == nil && string(old) == proxyURL+"\n" {
		return
	}
	_ = os.WriteFile(p, []byte(proxyURL+"\n"), 0o644)
}

// markProxy 读接管标记里记的代理地址；旧版标记内容是 "1"，返回空（退回旧判断）。
func (a *App) markProxy(name string) string { return takeover.MarkProxy(a.store.Dir(), name) }

func (a *App) dropMark(name string) {
	_ = os.Remove(filepath.Join(a.store.Dir(), name))
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

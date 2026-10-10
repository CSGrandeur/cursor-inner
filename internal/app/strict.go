package app

import "cursor-inner/internal/egress"

// SetStrictEgress 以管理员身份（UAC）开启或关闭严格模式。代理地址取与 Grok 相同的那个，
// 脚本用它做 DoH 解析官方域名。
func (a *App) SetStrictEgress(enabled bool) error {
	proxyURL := ""
	if cfg := a.store.Get(); cfg.TakeoverGrok {
		proxyURL, _ = a.grokProxy(cfg)
	}
	return egress.Start(enabled, proxyURL)
}

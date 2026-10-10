package app

import (
	"net/url"
	"strconv"
	"strings"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/tunnel"
)

// SetTunnel 开启/关闭 sing-box TUN：只有官方域名经 TUN 转到当前代理，其它流量不碰。
// 失败一律返回错误并保留普通代理接管（env/flags/桥），绝不阻断启动或改坏网络。
func (a *App) SetTunnel(enabled bool) error {
	if !enabled {
		return a.tun.Disable()
	}
	cfg := a.store.Get()
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil || !on {
		return i18n.E("TUN 需要先配置并开启代理", "TUN needs the proxy configured and on")
	}
	opts, err := tunnelOptions(spec)
	if err != nil {
		return err
	}
	return a.tun.Enable(opts)
}

// tunnelOptions 把代理地址解析成 sing-box 出站参数，并探测当前真实 DNS 作为上游。
func tunnelOptions(spec string) (tunnel.Options, error) {
	s := strings.TrimSpace(spec)
	if !strings.Contains(s, "://") {
		s = "socks5://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return tunnel.Options{}, err
	}
	ptype := "socks"
	if strings.HasPrefix(strings.ToLower(u.Scheme), "http") {
		ptype = "http"
	}
	port, _ := strconv.Atoi(u.Port())
	o := tunnel.Options{ProxyType: ptype, ProxyHost: u.Hostname(), ProxyPort: port, DNSUpstreams: tunnel.DetectDNS()}
	if u.User != nil {
		o.ProxyUser = u.User.Username()
		o.ProxyPass, _ = u.User.Password()
	}
	return o, nil
}

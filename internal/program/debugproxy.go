package program

import (
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
)

// inheritProxy 让调试实例沿用用户配置的出站代理：调试数据目录里没开代理时，
// 拷入 src（用户正式配置）的代理设置。只拷代理，不碰模型、接管等其他设置。
// 返回生效的代理地址；两边都没配代理时返回空，此时才直连。
func inheritProxy(store *config.Store, src config.File) string {
	if spec, on, err := dialer.EffectiveAddress(store.Get().Proxy); err == nil && on {
		return spec
	}
	spec, on, err := dialer.EffectiveAddress(src.Proxy)
	if err != nil || !on {
		return ""
	}
	if err := store.Update(func(f *config.File) error {
		f.Proxy = src.Proxy
		return nil
	}); err != nil {
		return ""
	}
	return spec
}

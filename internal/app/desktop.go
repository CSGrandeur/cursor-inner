package app

import (
	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/tunnel"
	"cursor-inner/internal/web"
)

// SetTray 由 program 在通知区域图标挂上之后调用：配置页据此显示“后台运行”“通知”两行；
// apply 在设置变化后调用，让控制台窗口立即按新设置显示或隐藏。
func (a *App) SetTray(apply func(config.Desktop)) {
	a.mu.Lock()
	a.trayApply = apply
	a.mu.Unlock()
}

func (a *App) desktopView(cfg config.File) web.DesktopView {
	a.mu.Lock()
	tray := a.trayApply != nil
	a.mu.Unlock()
	return web.DesktopView{Tray: tray, Background: cfg.Desktop.Background, Notify: !cfg.Desktop.Quiet}
}

// SetDesktop 改“后台运行”和“通知”。nil 表示不改。
func (a *App) SetDesktop(background, notify *bool) error {
	a.mu.Lock()
	apply := a.trayApply
	a.mu.Unlock()
	if apply == nil {
		return i18n.E("此系统没有通知区域图标", "The notification area icon is not available on this system")
	}
	var next config.Desktop
	if err := a.store.Update(func(f *config.File) error {
		if background != nil {
			f.Desktop.Background = *background
		}
		if notify != nil {
			f.Desktop.Quiet = !*notify
		}
		next = f.Desktop
		return nil
	}); err != nil {
		return err
	}
	apply(next)
	return nil
}

// Alert 是托盘图标要变成“警告”的原因。Leak 表示官方端点可能泄漏或已经带出了自定义模型，值得弹通知。
type Alert struct {
	Text i18n.Text
	Leak bool
}

// Alerts 汇总当前需要用户注意的问题：接管或代理出错、开机启动写入失败、开了 TUN 却回退。
// 自定义模型被带给官方的事件由 mitm 计数，program 另外看。
func (a *App) Alerts() []Alert {
	var out []Alert
	if p := a.life.Snapshot().Problem; !p.IsZero() {
		out = append(out, Alert{Text: p})
	}
	if _, _, err := dialer.EffectiveAddress(a.store.Get().Proxy); err != nil {
		out = append(out, Alert{Text: i18n.Of(err)})
	}
	a.mu.Lock()
	boot := a.bootErr
	a.mu.Unlock()
	if !boot.IsZero() {
		out = append(out, Alert{Text: boot})
	}
	if st, _ := a.tun.Status(); st == tunnel.StateFailed {
		out = append(out, Alert{Text: st.Text(), Leak: true})
	}
	return out
}

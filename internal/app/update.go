package app

import (
	"context"
	"errors"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/selfupdate"
)

// SetUpdater 挂上自更新。没挂时配置页不显示更新区。
func (a *App) SetUpdater(u *selfupdate.Updater) { a.upd = u }

// UpdateProxy 给更新器用：配置了出站代理时返回它的拨号函数（默认路径失败时才用）。
func (a *App) UpdateProxy() (selfupdate.DialFunc, bool) {
	cfg := a.store.Get()
	if _, on, err := dialer.EffectiveAddress(cfg.Proxy); err != nil || !on {
		return nil, false
	}
	d, err := dialer.FromProxy(cfg.Proxy)
	if err != nil || d == nil {
		return nil, false
	}
	return selfupdate.DialFunc(d), true
}

// Freeze 在交接开始时停掉会重新监听或改 Grok 参数的后台同步，避免和新进程抢端口。
func (a *App) Freeze(on bool) { a.frozen.Store(on) }

// ReleaseBridge / ReclaimBridge 交出、收回 Grok 代理桥的监听。
func (a *App) ReleaseBridge() error { return a.bridge.Release() }
func (a *App) ReclaimBridge() error { return a.bridge.Reclaim() }

var errNoUpdater = errors.New("这个版本没有启用自动更新")

func (a *App) UpdateStatus() (selfupdate.Status, error) {
	if a.upd == nil {
		return selfupdate.Status{}, errNoUpdater
	}
	return a.upd.Status(), nil
}

// CheckUpdate 检查一次。auto 为真是配置页打开时的启动检查，每个进程只跑一次。
func (a *App) CheckUpdate(auto bool) (selfupdate.Status, error) {
	if a.upd == nil {
		return selfupdate.Status{}, errNoUpdater
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return a.upd.Check(ctx, auto), nil
}

// ApplyUpdate 在后台下载并交接，立即返回；进度看 UpdateStatus。
func (a *App) ApplyUpdate() error {
	if a.upd == nil {
		return errNoUpdater
	}
	if st := a.upd.Status(); st.State != selfupdate.StateAvailable {
		return errors.New("没有可用的更新，先检查更新")
	}
	go func() {
		// 等自定义模型回合结束最多 30 分钟，之后推迟。
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		_ = a.upd.Apply(ctx)
	}()
	return nil
}

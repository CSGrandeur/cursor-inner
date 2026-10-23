package app

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/logx"
	"cursor-inner/internal/proxywait"
)

// proxyWaitSchedule 方便测试缩短。
var proxyWaitSchedule = proxywait.Default

// proxyWait 是“等代理开始监听”的状态（见 proxywait）。代理一就绪就补做接管和推迟的自动检查更新。
func (a *App) proxyWait() *proxywait.Waiter {
	a.pwOnce.Do(func() {
		a.pw = proxywait.New(proxyWaitSchedule)
		pw := a.pw
		dialer.OnProxyDialError(pw.Kick)
		a.pw.OnReady(func() {
			a.SyncGrok()
			a.SyncCodex()
			if a.updPending.Swap(false) && a.upd != nil {
				slog.Info("代理已就绪，补做推迟的自动检查更新", logx.FileOnly)
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
					defer cancel()
					a.upd.Check(ctx, true)
				}()
			}
		})
	})
	return a.pw
}

// WatchProxy 按当前设置开始（或停止）等代理。启动时和改代理地址后调用。
func (a *App) WatchProxy() {
	cfg := a.store.Get()
	spec, on, err := dialer.EffectiveAddress(cfg.Proxy)
	if err != nil || !on {
		a.proxyWait().Stop()
		return
	}
	host := proxyHostPort(spec)
	if host == "" {
		a.proxyWait().Stop()
		return
	}
	a.proxyWait().Start(host, proxywait.Probe(host))
}

// proxyReady：代理在监听，或者没开代理（不用等）。
func (a *App) proxyReady() bool { return a.proxyWait().Ready() }

func proxyHostPort(spec string) string {
	if !strings.Contains(spec, "://") {
		return spec
	}
	u, err := url.Parse(spec)
	if err != nil {
		return ""
	}
	return u.Host
}

// proxyWaitView 给配置页：等待中（waiting）或超时（down）时返回状态、地址和开始时间。
func (a *App) proxyWaitView() (wait, addr, since string) {
	st, ad, t := a.proxyWait().State()
	if st != proxywait.Waiting && st != proxywait.Down {
		return "", "", ""
	}
	return st.String(), ad, t.UTC().Format(time.RFC3339)
}

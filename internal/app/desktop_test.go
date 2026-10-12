package app

import (
	"testing"

	"cursor-inner/internal/agent"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/tunnel"
)

func desktopApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := mitm.New(func() config.Proxy { return store.Get().Proxy }, func() []catalog.Entry { return nil },
		func(string) (config.Model, bool) { return config.Model{}, false }, agent.NewHistory(dir))
	return &App{store: store, life: takeover.New(dir, m), boot: nopBoot{}, tun: tunnel.NewManager(dir)}
}

// 托盘没挂上（非 Windows）：配置页不显示这两行，接口拒绝修改。挂上后改动落盘并立即交给托盘。
func TestSetDesktop(t *testing.T) {
	a := desktopApp(t)
	view, _ := a.State()
	if view.Desktop.Tray || !view.Desktop.Notify || view.Desktop.Background {
		t.Fatalf("defaults: %+v", view.Desktop)
	}
	on := true
	if err := a.SetDesktop(&on, nil); err == nil {
		t.Fatal("must refuse without a tray")
	}
	var applied []config.Desktop
	a.SetTray(func(d config.Desktop) { applied = append(applied, d) })
	if err := a.SetDesktop(&on, nil); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := a.SetDesktop(nil, &off); err != nil {
		t.Fatal(err)
	}
	view, _ = a.State()
	if !view.Desktop.Tray || !view.Desktop.Background || view.Desktop.Notify {
		t.Fatalf("view: %+v", view.Desktop)
	}
	if len(applied) != 2 || !applied[0].Background || applied[0].Quiet || !applied[1].Quiet {
		t.Fatalf("applied: %+v", applied)
	}
	if got := a.store.Get().Desktop; !got.Background || !got.Quiet {
		t.Fatalf("stored: %+v", got)
	}
}

func TestAlerts(t *testing.T) {
	a := desktopApp(t)
	if got := a.Alerts(); len(got) != 0 {
		t.Fatalf("clean state: %+v", got)
	}
	_ = a.store.Update(func(f *config.File) error { f.Proxy = config.Proxy{Enabled: true, Address: "ftp://x:1"}; return nil })
	got := a.Alerts()
	if len(got) != 1 || got[0].Leak || got[0].Text.IsZero() {
		t.Fatalf("bad proxy: %+v", got)
	}
}

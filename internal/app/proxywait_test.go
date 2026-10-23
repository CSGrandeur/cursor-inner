package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/proxywait"
	"cursor-inner/internal/selfupdate"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func withFastWait(t *testing.T) {
	old := proxyWaitSchedule
	proxyWaitSchedule = proxywait.Schedule{Fast: time.Second, FastEvery: 20 * time.Millisecond, SlowEvery: 50 * time.Millisecond, Timeout: 300 * time.Millisecond}
	t.Cleanup(func() { proxyWaitSchedule = old })
}

func TestGrokTakeoverWaitsForProxyThenApplies(t *testing.T) {
	withFastWait(t)
	logs := &syncBuf{}
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLog) })

	addr := freePort(t)
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Update(func(f *config.File) error {
		f.TakeoverGrok = true
		f.Proxy = config.Proxy{Enabled: true, Address: addr, Scope: config.ScopeOfficial}
		return nil
	})
	var mu sync.Mutex
	var applied []string
	old := grokApply
	grokApply = func(p string) error { mu.Lock(); applied = append(applied, p); mu.Unlock(); return nil }
	t.Cleanup(func() { grokApply = old })
	a := &App{store: store}
	t.Cleanup(a.bridge.Close)

	a.WatchProxy()
	a.SyncGrok()
	if w, _, _ := a.proxyWaitView(); w != "waiting" {
		t.Fatalf("closed proxy at startup must show waiting, got %q", w)
	}
	mu.Lock()
	n := len(applied)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("Grok must not be touched while the proxy is not listening: %v", applied)
	}
	if strings.Contains(logs.String(), "代理不可用") || strings.Contains(logs.String(), "没法用当前代理") {
		t.Fatalf("waiting must not log errors: %s", logs.String())
	}
	// 超时后才是异常，并提示打开代理软件
	deadline := time.Now().Add(3 * time.Second)
	for {
		if w, _, _ := a.proxyWaitView(); w == "down" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never reported down after the timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "请打开你的代理软件") {
		t.Fatalf("timeout must tell the user what to do: %s", logs.String())
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	deadline = time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n = len(applied)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Grok takeover was not applied after the proxy came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := applied[0]
	mu.Unlock()
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Fatalf("expected loopback bridge, got %q", got)
	}
	if w, _, _ := a.proxyWaitView(); w != "" {
		t.Fatalf("ready proxy must clear waiting, got %q", w)
	}
}

func TestAutoUpdateCheckDeferredUntilProxyReady(t *testing.T) {
	withFastWait(t)
	addr := freePort(t)
	store, _ := config.Load(t.TempDir())
	store.Update(func(f *config.File) error {
		f.Proxy = config.Proxy{Enabled: true, Address: addr}
		return nil
	})
	a := &App{store: store}
	a.upd = selfupdate.New(selfupdate.Options{Version: "0.0.1", DataDir: t.TempDir(), Sources: []selfupdate.Source{selfupdate.URLSource("http://127.0.0.1:1/manifest.json")}})
	a.WatchProxy()
	st, _ := a.CheckUpdate(true)
	if st.State == selfupdate.StateFailed || st.State == selfupdate.StateChecking {
		t.Fatalf("auto check must be deferred while waiting for the proxy, got %q", st.State)
	}
	if !a.updPending.Load() {
		t.Fatal("deferred check must be remembered")
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	deadline := time.Now().Add(5 * time.Second)
	for a.upd.Status().CheckedAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("deferred auto check never ran after the proxy came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = context.Background
}

// 代理中途消失：经代理拨号失败后回到等待（不动 Grok），代理回来后自动重新接上并清除等待。
func TestProxyLostMidRunWaitsThenReappliesGrok(t *testing.T) {
	old := proxyWaitSchedule
	proxyWaitSchedule = proxywait.Schedule{Fast: time.Second, FastEvery: 20 * time.Millisecond, SlowEvery: 50 * time.Millisecond, Timeout: 300 * time.Millisecond, Health: time.Hour, Debounce: 30 * time.Millisecond}
	t.Cleanup(func() { proxyWaitSchedule = old })
	addr := freePort(t)
	serve := func() net.Listener {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		return l
	}
	l := serve()
	store, _ := config.Load(t.TempDir())
	store.Update(func(f *config.File) error {
		f.TakeoverGrok = true
		f.Proxy = config.Proxy{Enabled: true, Address: "socks5://" + addr, Scope: config.ScopeOfficial}
		return nil
	})
	var mu sync.Mutex
	applied := 0
	oldApply := grokApply
	grokApply = func(string) error { mu.Lock(); applied++; mu.Unlock(); return nil }
	t.Cleanup(func() { grokApply = oldApply })
	a := &App{store: store}
	t.Cleanup(a.bridge.Close)
	a.WatchProxy()
	count := func() int { mu.Lock(); defer mu.Unlock(); return applied }
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("grok not applied while proxy up", func() bool { return count() >= 1 })
	before := count()

	l.Close()
	d, _ := dialer.FromProxy(store.Get().Proxy)
	if c, err := d(context.Background(), "tcp", "api2.cursor.sh:443"); err == nil {
		c.Close()
		t.Fatal("official dial must fail while the proxy is gone")
	}
	waitFor("lost proxy did not go back to waiting", func() bool { w, _, _ := a.proxyWaitView(); return w == "waiting" })
	a.SyncGrok()
	if count() != before {
		t.Fatal("Grok must not be touched while waiting for the proxy")
	}
	l = serve()
	defer l.Close()
	waitFor("waiting not cleared after the proxy came back", func() bool { w, _, _ := a.proxyWaitView(); return w == "" })
	waitFor("Grok takeover not re-applied after recovery", func() bool { return count() > before })
}

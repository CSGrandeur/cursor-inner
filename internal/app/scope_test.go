package app

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/autostart"
	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/takeover"
	"cursor-inner/internal/tunnel"
)

type nopBoot struct{}

func (nopBoot) Apply(bool, string) (autostart.State, error) { return autostart.State{}, nil }
func (nopBoot) Current(string) autostart.State              { return autostart.State{} }

// upstream 是记录 CONNECT 目标的假上游代理。
func upstream(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, req.Host)
				mu.Unlock()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				_, _ = io.Copy(c, br)
			}()
		}
	}()
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// connectVia 经 Grok 用的代理地址 CONNECT 到 target，发一句话，读回显。
func connectVia(t *testing.T, proxyURL, target string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(proxyURL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT %s: %v %v", target, err, resp)
	}
	_, _ = io.WriteString(c, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo via %s: %q %v", target, buf, err)
	}
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// 带认证（或 socks）的代理本来就经代理桥：仅官方时桥里官方走代理、回环直连；切回全局后
// 同一个桥地址全部走代理，Grok Bot 的启动参数不变（不用重开）。SetProxy 改地址不会把范围冲掉。
func TestGrokBridgeFollowsScopeWithoutRestart(t *testing.T) {
	plain, seen := upstream(t)
	proxyURL := strings.Replace(plain, "http://", "http://u:p@", 1)
	echo := echoServer(t)
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(f *config.File) error {
		f.TakeoverGrok = true
		f.Proxy = config.Proxy{Enabled: true, Address: proxyURL, Scope: config.ScopeOfficial}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{store: store}
	a.Isolate()
	defer a.bridge.Close()

	grokURL, keep := a.grokProxy(store.Get())
	if keep || grokURL == plain || !strings.HasPrefix(grokURL, "http://127.0.0.1:") {
		t.Fatalf("official scope must hand Grok the loopback bridge, got %q keep=%v", grokURL, keep)
	}
	connectVia(t, grokURL, "api.x.ai:443")
	connectVia(t, grokURL, echo)
	if got := seen(); len(got) != 1 || got[0] != "api.x.ai:443" {
		t.Fatalf("official scope: upstream saw %v, want only api.x.ai:443", got)
	}

	if err := a.SetProxyScope(config.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	connectVia(t, grokURL, echo)
	if got := seen(); len(got) != 2 || got[1] != echo {
		t.Fatalf("global scope through the same bridge: upstream saw %v", got)
	}

	if err := a.SetProxyScope(config.ScopeOfficial); err != nil {
		t.Fatal(err)
	}
	if again, _ := a.grokProxy(store.Get()); again != grokURL {
		t.Fatalf("bridge address changed: %q -> %q", grokURL, again)
	}
	if err := a.SetProxyScope("everything"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

// 普通 HTTP 代理：全局时直接交给 Grok Bot；仅官方时改走代理桥（会重开一次 Grok Bot）。
// 切回全局后，还连着旧桥的请求也按全局走代理。
func TestGrokPlainHTTPProxyBridgesOnlyWhenOfficial(t *testing.T) {
	plain, seen := upstream(t)
	echo := echoServer(t)
	a := &App{}
	defer a.bridge.Close()
	official := config.File{TakeoverGrok: true, Proxy: config.Proxy{Enabled: true, Address: plain, Scope: config.ScopeOfficial}}
	bridgeURL, keep := a.grokProxy(official)
	if keep || bridgeURL == plain {
		t.Fatalf("official scope: got %q keep=%v", bridgeURL, keep)
	}
	connectVia(t, bridgeURL, echo)
	if got := seen(); len(got) != 0 {
		t.Fatalf("loopback went through the proxy: %v", got)
	}
	global := official
	global.Proxy.Scope = config.ScopeGlobal
	if u, keep := a.grokProxy(global); keep || u != plain {
		t.Fatalf("global scope: got %q keep=%v, want %q", u, keep, plain)
	}
	connectVia(t, bridgeURL, echo)
	if got := seen(); len(got) != 1 || got[0] != echo {
		t.Fatalf("stale bridge after switching to global: upstream saw %v", got)
	}
}

// 在配置页只改代理地址或开关，不能把代理范围冲回全局。
func TestSetProxyKeepsScope(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := mitm.New(func() config.Proxy { return store.Get().Proxy }, func() []catalog.Entry { return nil }, func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	a := &App{store: store, life: takeover.New(dir, m), boot: nopBoot{}, tun: tunnel.NewManager(dir)}
	a.Isolate()
	defer a.bridge.Close()
	if err := a.SetProxyScope(config.ScopeOfficial); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProxy(true, "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Proxy; got.ScopeName() != config.ScopeOfficial || got.Address != "http://127.0.0.1:9" {
		t.Fatalf("SetProxy lost the scope: %+v", got)
	}
	view, _ := a.State()
	if view.Proxy.Scope != config.ScopeOfficial || view.Proxy.Bridged {
		t.Fatalf("view: %+v", view.Proxy)
	}
}

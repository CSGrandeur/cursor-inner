package dialer

import (
	"net"
	"testing"
)

// 保存时只抽 host:port，不假定协议：不带协议、http、socks5、socks5h、IPv6 都能判断；指向自己端口要拒绝。
func TestProxyHostPortAllForms(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"127.0.0.1:1080", "127.0.0.1", "1080"},
		{"http://127.0.0.1:1080", "127.0.0.1", "1080"},
		{"socks5://127.0.0.1:1080", "127.0.0.1", "1080"},
		{"socks5h://user:pw@127.0.0.1:1080", "127.0.0.1", "1080"},
		{"[::1]:1080", "::1", "1080"},
		{"socks5://[::1]:1080", "::1", "1080"},
		{"Proxy.Example:8080", "proxy.example", "8080"},
	}
	for _, c := range cases {
		h, p, err := HostPort(c.in)
		if err != nil || h != c.host || p != c.port {
			t.Fatalf("HostPort(%q) = %q %q %v", c.in, h, p, err)
		}
	}
	for _, bad := range []string{"", "127.0.0.1", "http://127.0.0.1", "::1"} {
		if _, _, err := HostPort(bad); err == nil {
			t.Fatalf("HostPort(%q) should fail", bad)
		}
	}
}

func TestRejectSelfAllForms(t *testing.T) {
	self := "http://127.0.0.1:7777"
	for _, ok := range []string{"127.0.0.1:1080", "http://127.0.0.1:1080", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080", "[::1]:1080"} {
		if err := RejectSelf(ok, self); err != nil {
			t.Fatalf("RejectSelf(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1:7777", "localhost:7777", "socks5://127.0.0.1:7777", "socks5h://localhost:7777", "[::1]:7777", "http://[::1]:7777"} {
		if err := RejectSelf(bad, self); err == nil {
			t.Fatalf("RejectSelf(%q) must reject our own port", bad)
		}
	}
}

// 不带协议的地址由探测决定协议：假 socks5 → socks5://，假 http → http://；写了协议就照用，不探测。
func TestResolveProbesBareAddress(t *testing.T) {
	socks := fakeServer(t, func(c net.Conn) { buf := make([]byte, 3); _, _ = c.Read(buf); _, _ = c.Write([]byte{0x05, 0x00}) })
	httpAddr := fakeServer(t, func(c net.Conn) {
		buf := make([]byte, 3)
		_, _ = c.Read(buf)
		_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	})
	if got := Resolve(socks); got != "socks5://"+socks {
		t.Fatalf("bare socks5: %q", got)
	}
	if got := Resolve(httpAddr); got != "http://"+httpAddr {
		t.Fatalf("bare http: %q", got)
	}
	if got := Resolve("socks5://" + httpAddr); got != "socks5://"+httpAddr {
		t.Fatalf("explicit scheme must be kept: %q", got)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	if got := Resolve(dead); got != dead {
		t.Fatalf("unreachable bare address must stay as written: %q", got)
	}
}

func fakeServer(t *testing.T, serve func(net.Conn)) string {
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
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return ln.Addr().String()
}

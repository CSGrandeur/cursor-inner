package mitm

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func recordingProxy(t *testing.T) (string, func() []string) {
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

// tunnel 经本机代理 CONNECT 到 target，先发 first（如 ClientHello），读回同样多的回显。
func tunnel(t *testing.T, mitmURL, target string, first []byte) {
	t.Helper()
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(mitmURL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT %s: %v", target, err)
	}
	if _, err := c.Write(first); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != string(first) {
		t.Fatalf("echo via %s failed: %v", target, err)
	}
}

func clientHello(t *testing.T, sni string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		c := tls.Client(a, &tls.Config{ServerName: sni})
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_ = c.Handshake()
	}()
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := b.Read(buf)
		out = append(out, buf[:n]...)
		if len(out) >= 5 && len(out) >= 5+(int(out[3])<<8|int(out[4])) {
			return out
		}
		if err != nil {
			t.Fatalf("hello: %v", err)
		}
	}
}

// Cursor 的本机代理按代理范围拨号，每次拨号读当前设置：切换范围对新连接立即生效。
// 仅官方：官方主机、带官方 SNI 的连接走代理，回环直连；全局：全部走代理。
func TestCursorTunnelsFollowProxyScope(t *testing.T) {
	proxyURL, seen := recordingProxy(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	var mu sync.Mutex
	cfg := config.Proxy{Enabled: true, Address: proxyURL, Scope: config.ScopeOfficial}
	get := func() config.Proxy { mu.Lock(); defer mu.Unlock(); return cfg }
	s := New(get, func() []catalog.Entry { return nil }, func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	url, err := s.Start(testCA(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	tunnel(t, url, "api.x.ai:443", []byte("ping"))
	tunnel(t, url, echo.Addr().String(), clientHello(t, "book.example.com"))
	if got := seen(); len(got) != 1 || got[0] != "api.x.ai:443" {
		t.Fatalf("official scope: upstream saw %v", got)
	}

	mu.Lock()
	cfg.Scope = config.ScopeGlobal
	mu.Unlock()
	tunnel(t, url, echo.Addr().String(), []byte("ping"))
	if got := seen(); len(got) != 2 || got[1] != echo.Addr().String() {
		t.Fatalf("global scope: upstream saw %v", got)
	}
}

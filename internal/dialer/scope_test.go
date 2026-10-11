package dialer

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/config"
)

// route 记下每次拨号走了哪条路，对端把收到的字节原样回显。
type route struct {
	mu    sync.Mutex
	calls []string
	fail  error
}

func (r *route) dial(name string) Func {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		r.mu.Lock()
		r.calls = append(r.calls, name+" "+addr)
		r.mu.Unlock()
		if r.fail != nil && name == "direct" {
			return nil, r.fail
		}
		a, b := net.Pipe()
		chunks := make(chan []byte, 64)
		go func() {
			defer close(chunks)
			buf := make([]byte, 32<<10)
			for {
				n, err := b.Read(buf)
				if n > 0 {
					chunks <- append([]byte(nil), buf[:n]...)
				}
				if err != nil {
					return
				}
			}
		}()
		go func() {
			for p := range chunks {
				if _, err := b.Write(p); err != nil {
					return
				}
			}
		}()
		return a, nil
	}
}

func (r *route) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func hello(t *testing.T, sni string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		c := tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: sni == ""})
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

func TestSplitRoutesByTarget(t *testing.T) {
	cases := []struct {
		addr  string
		first []byte
		want  string
	}{
		{"api.x.ai:443", nil, "proxy"},
		{"grok.com:443", nil, "proxy"},
		{"us1.cursorvm.com:443", nil, "proxy"},
		{"198.18.0.9:443", nil, "proxy"},
		{"203.0.113.7:443", nil, "proxy"},
		{"127.0.0.1:8080", nil, "direct"},
		{"192.168.3.4:22", nil, "direct"},
		{"localhost:3000", nil, "direct"},
		{"book.example.com:443", hello(t, "book.example.com"), "direct"},
		{"book.example.com:443", hello(t, "api.x.ai"), "proxy"},
		{"book.example.com:443", hello(t, ""), "proxy"},
		{"book.example.com:80", []byte("GET / HTTP/1.1\r\nHost: book.example.com\r\n\r\n"), "direct"},
		{"book.example.com:80", []byte("GET / HTTP/1.1\r\nHost: grok.com\r\n\r\n"), "proxy"},
		{"book.example.com:5222", []byte("<stream:stream>"), "proxy"},
	}
	for _, c := range cases {
		r := &route{}
		d := split(r.dial("proxy"), r.dial("direct"))
		conn, err := d(context.Background(), "tcp", c.addr)
		if err != nil {
			t.Fatalf("%s: %v", c.addr, err)
		}
		payload := c.first
		if payload == nil {
			payload = []byte("ping")
		}
		// 分两次写，模拟 ClientHello 跨 TCP 段到达。
		half := len(payload) / 2
		if _, err := conn.Write(payload[:half]); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(payload[half:]); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, len(payload))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(conn, echo); err != nil {
			t.Fatalf("%s: read echo: %v", c.addr, err)
		}
		if string(echo) != string(payload) {
			t.Fatalf("%s: bytes changed in transit", c.addr)
		}
		conn.Close()
		calls := r.got()
		if len(calls) != 1 || calls[0] != c.want+" "+c.addr {
			t.Errorf("%s (%q...): calls %v, want %s", c.addr, firstN(c.first, 12), calls, c.want)
		}
	}
}

func firstN(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

func TestSplitSilentClientGoesThroughProxy(t *testing.T) {
	old := inspectWait
	inspectWait = 50 * time.Millisecond
	defer func() { inspectWait = old }()
	r := &route{}
	conn, _ := split(r.dial("proxy"), r.dial("direct"))(context.Background(), "tcp", "mail.example.com:25")
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err := conn.Read(make([]byte, 1))
	if !errors.Is(err, osDeadline()) && err == nil {
		t.Fatalf("read: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls := r.got(); len(calls) != 1 || !strings.HasPrefix(calls[0], "proxy ") {
		t.Fatalf("calls %v", calls)
	}
	conn.Close()
}

func osDeadline() error { return errDeadline }

var errDeadline = func() error {
	c := &inspectConn{ready: make(chan struct{})}
	c.rdl = time.Now()
	_, err := c.Read(nil)
	return err
}()

func TestSplitCloseBeforeDataDialsNothing(t *testing.T) {
	r := &route{}
	conn, _ := split(r.dial("proxy"), r.dial("direct"))(context.Background(), "tcp", "book.example.com:443")
	conn.Close()
	time.Sleep(50 * time.Millisecond)
	if calls := r.got(); len(calls) != 0 {
		t.Fatalf("dialed after close: %v", calls)
	}
}

func TestSplitDirectFailureNeverFallsBackToProxy(t *testing.T) {
	r := &route{fail: errors.New("no route")}
	conn, _ := split(r.dial("proxy"), r.dial("direct"))(context.Background(), "tcp", "book.example.com:443")
	if _, err := conn.Write(hello(t, "book.example.com")); err == nil {
		t.Fatal("write should fail when the direct dial fails")
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("read should fail too")
	}
	if calls := r.got(); len(calls) != 1 || !strings.HasPrefix(calls[0], "direct ") {
		t.Fatalf("calls %v", calls)
	}
}

// connectProxy 是记录 CONNECT 目标的本机 HTTP 代理，接通后回显。
func connectProxy(t *testing.T) (string, func() []string) {
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

func TestScopedGlobalVersusOfficial(t *testing.T) {
	addr, seen := connectProxy(t)
	global, err := Scoped(config.Proxy{Enabled: true, Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	c, err := global(context.Background(), "tcp", "book.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got := seen(); len(got) != 1 || got[0] != "book.invalid:443" {
		t.Fatalf("global: proxy saw %v", got)
	}

	r := &route{}
	old := directDial
	directDial = func() Func { return r.dial("direct") }
	defer func() { directDial = old }()
	only, err := Scoped(config.Proxy{Enabled: true, Address: addr, Scope: config.ScopeOfficial})
	if err != nil {
		t.Fatal(err)
	}
	c, err = only(context.Background(), "tcp", "api.x.ai:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// 第三方：直连，代理一条都不该收到。
	c, err = only(context.Background(), "tcp", "book.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(hello(t, "book.invalid")); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if calls := r.got(); len(calls) != 1 || calls[0] != "direct book.invalid:443" {
		t.Fatalf("direct calls %v", calls)
	}
	if got := seen(); len(got) != 2 || got[1] != "api.x.ai:443" {
		t.Fatalf("official: proxy saw %v", got)
	}

	// 代理关了：两种范围都直连，Scoped 不报错。
	if _, err := Scoped(config.Proxy{Enabled: false, Address: addr, Scope: config.ScopeOfficial}); err != nil {
		t.Fatal(err)
	}
}

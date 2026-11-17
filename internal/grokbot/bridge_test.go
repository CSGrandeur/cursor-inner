package grokbot

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNeedsBridge(t *testing.T) {
	for spec, want := range map[string]bool{
		"127.0.0.1:1080":            false,
		"http://127.0.0.1:1080":     false,
		"http://u:p@127.0.0.1:1080": true,
		"socks5://127.0.0.1:1080":   true,
		"socks5h://u:p@[::1]:1080":  true,
	} {
		if got := NeedsBridge(spec); got != want {
			t.Errorf("%s: %v", spec, got)
		}
	}
}

func echoServer(t *testing.T) string {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
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

// authProxy 是要求 Proxy-Authorization 的 HTTP CONNECT 代理，记录收到的认证。
func authProxy(t *testing.T, user, pass string) (string, *atomic.Int32) {
	var authed atomic.Int32
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
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
				if req.Header.Get("Proxy-Authorization") != want {
					_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
					return
				}
				authed.Add(1)
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				go func() { _, _ = io.Copy(up, br) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), &authed
}

func connectThrough(t *testing.T, bridgeURL, target string) (string, error) {
	addr := strings.TrimPrefix(bridgeURL, "http://")
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", errors.New(resp.Status)
	}
	_, _ = io.WriteString(c, "ping\n")
	line, err := br.ReadString('\n')
	return line, err
}

// 端到端：桥把 CONNECT 交给带认证的上游代理，而不是自己连目标。
func TestBridgeAddsAuthViaUpstream(t *testing.T) {
	target := echoServer(t)
	proxyAddr, authed := authProxy(t, "u", "p@ss")
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		auth := base64.StdEncoding.EncodeToString([]byte("u:p@ss"))
		_, _ = io.WriteString(c, "CONNECT "+addr+" HTTP/1.1\r\nHost: "+addr+"\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n")
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil || resp.StatusCode != 200 {
			c.Close()
			return nil, errors.New("upstream refused")
		}
		return c, nil
	}
	var b Bridge
	defer b.Close()
	u, err := b.Ensure("http://u:p@ss@"+proxyAddr, dial)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatal(u)
	}
	got, err := connectThrough(t, u, target)
	if err != nil || got != "ping\n" || authed.Load() != 1 {
		t.Fatalf("got %q err %v authed %d", got, err, authed.Load())
	}
	again, err := b.Ensure("http://u:p@ss@"+proxyAddr, dial)
	if err != nil || again != u {
		t.Fatalf("same proxy must keep the same port: %s vs %s", again, u)
	}
}

// 上游代理不可用时，桥返回 502，不会自己去连目标。
func TestBridgeNeverFallsBackToDirect(t *testing.T) {
	var direct atomic.Int32
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			direct.Add(1)
			c.Close()
		}
	}()
	var b Bridge
	defer b.Close()
	u, err := b.Ensure("socks5://127.0.0.1:1", func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("proxy down")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connectThrough(t, u, ln.Addr().String()); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502, got %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if direct.Load() != 0 {
		t.Fatal("bridge dialed the target directly")
	}
}

func TestBridgeRejectsSelfTargetLoop(t *testing.T) {
	var dialed atomic.Int32
	var b Bridge
	defer b.Close()
	u, err := b.Ensure("antigravity\nhttp://127.0.0.1:1080", func(context.Context, string, string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("must not dial self")
	})
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(u, "http://")

	for _, rawReq := range []string{
		"GET " + u + "/bind?foo=1 HTTP/1.1\r\nHost: " + addr + "\r\n\r\n",
		"GET /bind?foo=1 HTTP/1.1\r\nHost: " + addr + "\r\n\r\n",
		"CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n\r\n",
	} {
		c, err := net.DialTimeout("tcp4", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(c, rawReq)
		resp, rerr := http.ReadResponse(bufio.NewReader(c), nil)
		c.Close()
		if rerr != nil {
			t.Fatalf("read response: %v", rerr)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", resp.StatusCode)
		}
	}
	if dialed.Load() != 0 {
		t.Fatalf("expected 0 dial calls on self target, got %d", dialed.Load())
	}
}

func TestBridgeEnsureAtPrefersManagedPortAndReportsBusy(t *testing.T) {
	extLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	prefPort := extLn.Addr().(*net.TCPAddr).Port
	extDone := make(chan struct{})
	go func() {
		defer close(extDone)
		for {
			c, err := extLn.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	var b Bridge
	defer b.Close()
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }

	u, busy, err := b.EnsureAt("antigravity\nhttp://127.0.0.1:1080", prefPort, dial)
	if err != nil || !busy || u != "" {
		t.Fatalf("while preferred port held: u=%q busy=%v err=%v", u, busy, err)
	}

	_ = extLn.Close()
	<-extDone

	u, busy, err = b.EnsureAt("antigravity\nhttp://127.0.0.1:1080", prefPort, dial)
	if err != nil || busy {
		t.Fatalf("after preferred port freed: u=%q busy=%v err=%v", u, busy, err)
	}
	if u != "http://"+extLn.Addr().String() {
		t.Fatalf("expected reclaimed preferred port %s, got %s", extLn.Addr().String(), u)
	}
}

type failWriteConn struct {
	net.Conn
	err error
}

func (f failWriteConn) Write([]byte) (int, error) { return 0, f.err }
func (f failWriteConn) Read([]byte) (int, error)  { return 0, f.err }
func (f failWriteConn) Close() error              { return nil }

func TestBridgePlainHTTPFailuresReturn502InsteadOfEmptyReset(t *testing.T) {
	// 1. 上游接受连接后一字节不回直接断开。
	emptyLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer emptyLn.Close()
	go func() {
		for {
			c, err := emptyLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = http.ReadRequest(bufio.NewReader(c))
			}(c)
		}
	}()

	// 2. 上游实为 HTTPS 端口，收到明文 HTTP 后回 7 字节 TLS Fatal Alert 并断开。
	tlsAlertLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tlsAlertLn.Close()
	go func() {
		for {
			c, err := tlsAlertLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x02, 0x46})
			}(c)
		}
	}()

	var b Bridge
	defer b.Close()
	u, err := b.Ensure("antigravity\nhttp://127.0.0.1:1080", func(_ context.Context, _, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "deferred-fail.example:") {
			return failWriteConn{err: errors.New("connection refused")}, nil
		}
		return net.Dial("tcp4", addr)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{
			name: "upstream-0byte-eof",
			raw:  "GET http://" + emptyLn.Addr().String() + "/ping HTTP/1.1\r\nHost: " + emptyLn.Addr().String() + "\r\n\r\n",
		},
		{
			name: "plain-http-to-tls-alert",
			raw:  "POST http://" + tlsAlertLn.Addr().String() + "/login HTTP/1.1\r\nHost: " + tlsAlertLn.Addr().String() + "\r\nContent-Length: 2\r\n\r\n{}",
		},
		{
			name: "deferred-dial-write-fail",
			raw:  "GET http://deferred-fail.example:8080/api HTTP/1.1\r\nHost: deferred-fail.example:8080\r\n\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := sendBridgeRaw(t, u, tc.raw)
			if err != nil {
				t.Fatalf("expected valid 502 HTTP response instead of disconnect: %v", err)
			}
			if status != http.StatusBadGateway {
				t.Fatalf("status=%d want 502", status)
			}
		})
	}
}

func TestBridgePlainHTTPWebSocketUpgrade(t *testing.T) {
	wsLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wsLn.Close()
	go func() {
		for {
			c, err := wsLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.Header.Get("Proxy-Authorization") != "" || req.Header.Get("Proxy-Connection") != "" {
					_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 12\r\n\r\nleaked-proxy")
					return
				}
				if req.URL.Path == "/deny" {
					_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 6\r\n\r\ndenied")
					time.Sleep(2 * time.Second) // 保持连接不主动关，验证桥不会死等
					return
				}
				if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") || !headerHasToken(req.Header, "Connection", "upgrade") {
					_, _ = io.WriteString(c, "HTTP/1.1 426 Upgrade Required\r\nContent-Length: 0\r\n\r\n")
					return
				}
				// 101 响应头与服务端首帧紧挨在同一写操作里发出，验证预读缓冲不丢帧。
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ok-key\r\n\r\nserver-ready\n")
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = io.WriteString(c, "echo:"+line)
				}
			}(c)
		}
	}()

	var b Bridge
	defer b.Close()
	u, err := b.Ensure("antigravity\nhttp://127.0.0.1:1080", func(_ context.Context, _, addr string) (net.Conn, error) {
		return net.Dial("tcp4", addr)
	})
	if err != nil {
		t.Fatal(err)
	}
	bridgeAddr := strings.TrimPrefix(u, "http://")
	wsTarget := wsLn.Addr().String()

	// 1. 101 升级后双向全双工通信。
	c, err := net.DialTimeout("tcp4", bridgeAddr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "GET http://"+wsTarget+"/ws HTTP/1.1\r\nHost: "+wsTarget+"\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\nProxy-Authorization: Basic dTpw\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read 101 response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != "ok-key" {
		t.Fatalf("unexpected upgrade response: %d %v", resp.StatusCode, resp.Header)
	}
	ready, err := br.ReadString('\n')
	if err != nil || ready != "server-ready\n" {
		t.Fatalf("expected immediate server frame, got %q err=%v", ready, err)
	}
	for _, msg := range []string{"frame-1\n", "frame-2\n"} {
		if _, err := io.WriteString(c, msg); err != nil {
			t.Fatal(err)
		}
		got, err := br.ReadString('\n')
		if err != nil || got != "echo:"+msg {
			t.Fatalf("got %q want %q (err=%v)", got, "echo:"+msg, err)
		}
	}

	// 2. 上游拒绝升级（403）且保持长连接时，桥立即按响应体长度收尾返回。
	c2, err := net.DialTimeout("tcp4", bridgeAddr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	_ = c2.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	_, _ = io.WriteString(c2, "GET http://"+wsTarget+"/deny HTTP/1.1\r\nHost: "+wsTarget+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	resp2, err := http.ReadResponse(bufio.NewReader(c2), nil)
	if err != nil {
		t.Fatalf("read 403 response: %v", err)
	}
	body, err := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if err != nil || resp2.StatusCode != http.StatusForbidden || string(body) != "denied" {
		t.Fatalf("status=%d body=%q err=%v", resp2.StatusCode, body, err)
	}
}

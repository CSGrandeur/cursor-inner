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

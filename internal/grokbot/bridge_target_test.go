package grokbot

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readProxyReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestProxyDialTarget(t *testing.T) {
	tests := []struct {
		name, raw, wantTarget, wantSrc string
		ok                             bool
	}{
		{
			name:       "connect-authority",
			raw:        "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
			wantTarget: "example.com:443",
			wantSrc:    "connect-authority",
			ok:         true,
		},
		{
			name:       "connect-authority-no-host-header",
			raw:        "CONNECT example.com:443 HTTP/1.1\r\n\r\n",
			wantTarget: "example.com:443",
			wantSrc:    "connect-authority",
			ok:         true,
		},
		{
			name:       "absolute-form-http",
			raw:        "GET http://example.com/foo HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantTarget: "example.com:80",
			wantSrc:    "absolute-uri",
			ok:         true,
		},
		{
			name:       "absolute-form-http-no-host-header",
			raw:        "GET http://example.com/bar HTTP/1.1\r\n\r\n",
			wantTarget: "example.com:80",
			wantSrc:    "absolute-uri",
			ok:         true,
		},
		{
			name:       "absolute-form-https",
			raw:        "GET https://example.com/foo HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantTarget: "example.com:443",
			wantSrc:    "absolute-uri",
			ok:         true,
		},
		{
			name:       "origin-form-host-header",
			raw:        "GET /foo HTTP/1.1\r\nHost: example.com\r\n\r\n",
			wantTarget: "example.com:80",
			wantSrc:    "host-header",
			ok:         true,
		},
		{
			name:       "origin-form-host-with-port",
			raw:        "GET /foo HTTP/1.1\r\nHost: example.com:8443\r\n\r\n",
			wantTarget: "example.com:8443",
			wantSrc:    "host-header",
			ok:         true,
		},
		{
			name:       "websocket-origin-form",
			raw:        "GET /ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n",
			wantTarget: "example.com:80",
			wantSrc:    "host-header",
			ok:         true,
		},
		{
			name:       "missing-host-http10",
			raw:        "GET /foo HTTP/1.0\r\n\r\n",
			wantTarget: "",
			wantSrc:    "empty",
			ok:         false,
		},
		{
			name:       "http2-preface",
			raw:        "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n",
			wantTarget: "",
			wantSrc:    "http2-preface",
			ok:         false,
		},
		{
			name:       "connect-empty-host",
			raw:        "CONNECT :443 HTTP/1.1\r\n\r\n",
			wantTarget: "",
			wantSrc:    "connect-authority",
			ok:         false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := readProxyReq(t, tc.raw)
			got, src, ok := proxyDialTarget(req)
			if ok != tc.ok || got != tc.wantTarget {
				t.Fatalf("got target=%q src=%q ok=%v want target=%q src=%q ok=%v", got, src, ok, tc.wantTarget, tc.wantSrc, tc.ok)
			}
			if src != tc.wantSrc {
				t.Fatalf("src=%q want %q (target=%q ok=%v)", src, tc.wantSrc, got, ok)
			}
			if !ok && (got == ":80" || strings.HasPrefix(got, ":")) {
				t.Fatalf("empty target must not become %q", got)
			}
		})
	}
}

func sendBridgeRaw(t *testing.T, bridgeURL, raw string) (status int, err error) {
	t.Helper()
	addr := strings.TrimPrefix(bridgeURL, "http://")
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, raw); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func TestBridgeRejectsEmptyTargetWithoutDial(t *testing.T) {
	var dialed atomic.Int32
	var b Bridge
	defer b.Close()
	u, err := b.Ensure("socks5://127.0.0.1:1", func(context.Context, string, string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("should not dial empty target")
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		wantHTTP  bool
	}{
		{"http10-missing-host", "GET /foo HTTP/1.0\r\n\r\n", true},
		{"http2-preface", "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", true},
		{"socks5-client-hello", "\x05\x01\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialed.Store(0)
			status, err := sendBridgeRaw(t, u, tc.raw)
			if dialed.Load() != 0 {
				t.Fatalf("dialed %d times; empty/invalid request must not be proxied", dialed.Load())
			}
			if tc.wantHTTP {
				if err != nil {
					t.Fatal(err)
				}
				if status != http.StatusBadRequest {
					t.Fatalf("status=%d want 400", status)
				}
			}
		})
	}
}

func TestBridgeOriginAndAbsoluteFormDialHost(t *testing.T) {
	var gotAddr atomic.Value
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				_ = req
				_, _ = io.WriteString(c, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")
			}(c)
		}
	}()
	var b Bridge
	defer b.Close()
	u, err := b.Ensure("socks5://127.0.0.1:1", func(_ context.Context, _, addr string) (net.Conn, error) {
		gotAddr.Store(addr)
		return net.Dial("tcp", upstream.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"origin-form", "GET /foo HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com:80"},
		{"absolute-form", "GET http://example.com/foo HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com:80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotAddr.Store("")
			status, err := sendBridgeRaw(t, u, tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if status != http.StatusNoContent {
				t.Fatalf("status=%d want 204", status)
			}
			addr, _ := gotAddr.Load().(string)
			if addr != tc.want {
				t.Fatalf("dialed %q want %q", addr, tc.want)
			}
		})
	}
}

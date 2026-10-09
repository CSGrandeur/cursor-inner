package dialer

import (
	"context"
	"io"
	"net"
	"testing"

	"cursor-inner/internal/config"
)

func TestNormalizeBareHostKeepsNoScheme(t *testing.T) {
	got, err := Normalize("127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:1080" {
		t.Fatal(got)
	}
	got, err = Normalize("socks5://127.0.0.1:1080")
	if err != nil || got != "socks5://127.0.0.1:1080" {
		t.Fatal(got, err)
	}
	got, err = Normalize("http://127.0.0.1:1080")
	if err != nil || got != "http://127.0.0.1:1080" {
		t.Fatal(got, err)
	}
}

func TestEmptyAddressIsOffEvenWhenEnabled(t *testing.T) {
	_, on, err := EffectiveAddress(config.Proxy{Enabled: true, Address: "  "})
	if err != nil || on {
		t.Fatalf("on=%v err=%v", on, err)
	}
	_, on, err = EffectiveAddress(config.Proxy{Enabled: false, Address: "127.0.0.1:1"})
	if err != nil || on {
		t.Fatalf("on=%v err=%v", on, err)
	}
}

func TestRejectSelf(t *testing.T) {
	err := RejectSelf("socks5://127.0.0.1:15721", "http://127.0.0.1:15721")
	if err == nil {
		t.Fatal("expected reject")
	}
	if err := RejectSelf("socks5://127.0.0.1:1080", "http://127.0.0.1:15721"); err != nil {
		t.Fatal(err)
	}
}

func TestClassifySocksAndHTTP(t *testing.T) {
	socks := serveOnce(t, func(c net.Conn) {
		buf := make([]byte, 3)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte{0x05, 0x00})
	})
	if got, err := classify(socks); err != nil || got != "socks5" {
		t.Fatal(got, err)
	}
	httpAddr := serveOnce(t, func(c net.Conn) {
		buf := make([]byte, 3)
		_, _ = io.ReadFull(c, buf)
		c.Close()
	})
	if got, err := classify(httpAddr); err != nil || got != "http" {
		t.Fatal(got, err)
	}
}

func TestHTTPProxyConnects(t *testing.T) {
	addr := serveOnce(t, func(c net.Conn) {
		buf := make([]byte, 256)
		n, _ := io.ReadAtLeast(c, buf, 4)
		text := string(buf[:n])
		if len(text) < 8 || text[:8] != "CONNECT " {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
		pong := make([]byte, 4)
		_, _ = io.ReadFull(c, pong)
		_, _ = c.Write([]byte("pong"))
	})
	dial, err := FromProxy(config.Proxy{Enabled: true, Address: "http://" + addr})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "pong" {
		t.Fatal(string(buf), err)
	}
}

func serveOnce(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		handle(c)
	}()
	return ln.Addr().String()
}

func TestForModelDirectWhenFlagOff(t *testing.T) {
	d, err := ForModel(config.Proxy{Enabled: true, Address: "127.0.0.1:1080"}, false)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	_, err = ForModel(config.Proxy{Enabled: true}, true)
	if err == nil {
		t.Fatal("expected error")
	}
}

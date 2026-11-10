package dialer

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"cursor-inner/internal/config"
)

func TestDialProxyTCPRetriesRefusedThenSucceeds(t *testing.T) {
	oldGap, oldBudget, oldDial := proxyRetryGap, proxyRetryBudget, proxyTCPDial
	proxyRetryGap, proxyRetryBudget = 20*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() {
		proxyRetryGap, proxyRetryBudget = oldGap, oldBudget
		proxyTCPDial = oldDial
		proxyRetryGate.Store(nil)
	})

	var n atomic.Int64
	OnProxyDialError(func() { n.Add(1) })
	t.Cleanup(func() { proxyFailHook.Store(nil) })

	var calls atomic.Int64
	proxyTCPDial = func(ctx context.Context, base *net.Dialer, network, addr string) (net.Conn, error) {
		c := calls.Add(1)
		if c < 3 {
			return nil, errors.New("dial tcp 127.0.0.1:19991: connect: connection refused")
		}
		c1, c2 := net.Pipe()
		go c2.Close()
		return c1, nil
	}

	base := &net.Dialer{Timeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dialProxyTCP(ctx, base, "tcp", "127.0.0.1:19991")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if calls.Load() < 3 {
		t.Fatalf("calls=%d want >=3", calls.Load())
	}
	if n.Load() < 2 {
		t.Fatalf("kick=%d want >=2 (refused attempts before success)", n.Load())
	}
}

func TestDialProxyTCPPermanentRefusedStillFailsNoDirect(t *testing.T) {
	oldGap, oldBudget, oldDial := proxyRetryGap, proxyRetryBudget, proxyTCPDial
	proxyRetryGap, proxyRetryBudget = 20*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() {
		proxyRetryGap, proxyRetryBudget = oldGap, oldBudget
		proxyTCPDial = oldDial
		proxyRetryGate.Store(nil)
	})

	var calls atomic.Int64
	proxyTCPDial = func(ctx context.Context, base *net.Dialer, network, addr string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.")
	}

	base := &net.Dialer{Timeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := dialProxyTCP(ctx, base, "tcp", "127.0.0.1:1")
	if err == nil {
		c.Close()
		t.Fatal("expected failure, must not go direct")
	}
	if !isProxyPortRefused(err) {
		t.Fatalf("want refused, got %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("calls=%d want retries", calls.Load())
	}
}

func TestDialProxyTCPSkipsRetryWhenGateFalse(t *testing.T) {
	oldDial := proxyTCPDial
	OnProxyTransientRetry(func() bool { return false })
	t.Cleanup(func() {
		proxyTCPDial = oldDial
		proxyRetryGate.Store(nil)
	})

	var calls atomic.Int64
	proxyTCPDial = func(ctx context.Context, base *net.Dialer, network, addr string) (net.Conn, error) {
		calls.Add(1)
		return nil, syscall.ECONNREFUSED
	}

	base := &net.Dialer{Timeout: time.Second}
	_, err := dialProxyTCP(context.Background(), base, "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected failure")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d want 1 (no retry when Waiting/Down)", calls.Load())
	}
}

func TestFromProxyHTTPSurvivesBriefProxyPortRefused(t *testing.T) {
	oldGap, oldBudget := proxyRetryGap, proxyRetryBudget
	proxyRetryGap, proxyRetryBudget = 30*time.Millisecond, 800*time.Millisecond
	t.Cleanup(func() {
		proxyRetryGap, proxyRetryBudget = oldGap, oldBudget
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ready := make(chan struct{})
	go func() {
		time.Sleep(120 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err != nil {
			t.Log("relisten:", err)
			return
		}
		defer ln2.Close()
		close(ready)
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		defer c.Close()
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
		time.Sleep(50 * time.Millisecond)
	}()

	dial, err := FromProxy(config.Proxy{Enabled: true, Address: "http://" + addr})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("expected dial to succeed after brief refused: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "pong" {
		t.Fatal(string(buf), err)
	}
	select {
	case <-ready:
	default:
	}
}

func TestIsProxyPortRefused(t *testing.T) {
	if !isProxyPortRefused(syscall.ECONNREFUSED) {
		t.Fatal("ECONNREFUSED")
	}
	if !isProxyPortRefused(errors.New(`socks connect tcp 127.0.0.1:19991->api2.cursor.sh:443: connectex: No connection could be made because the target machine actively refused it.`)) {
		t.Fatal("actively refused")
	}
	if isProxyPortRefused(errors.New("socks5: general failure")) {
		t.Fatal("upstream socks failure must not look like local refused")
	}
	if isProxyPortRefused(errors.New("The proxy refused the connection: 403 Forbidden")) {
		t.Fatal("HTTP CONNECT reject after TCP ok must not retry as port refused")
	}
}

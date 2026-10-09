package mitm

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

func TestRecordTunnelCopiesBothDirections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte("pong"))
	}()
	var toNet, fromNet bytes.Buffer
	SetRaw(func(host, via string) (io.WriteCloser, io.WriteCloser) {
		if host != ln.Addr().String() || via != "proxy" {
			t.Errorf("raw %s %s", via, host)
		}
		return &memClose{Buffer: &toNet}, &memClose{Buffer: &fromNet}
	}, nil)
	t.Cleanup(func() { SetRaw(nil, nil) })

	s := New(func() config.Proxy { return config.Proxy{} }, func() []catalog.Entry { return nil }, nil, nil)
	client, server := net.Pipe()
	defer client.Close()
	req := httptest.NewRequest(http.MethodConnect, "http://"+ln.Addr().String(), nil)
	req.URL.Host = ln.Addr().String()
	done := make(chan struct{})
	go func() {
		s.recordTunnel(req, server, nil)
		close(done)
	}()
	br := bufio.NewReader(client)
	if line, err := br.ReadString('\n'); err != nil || line != "HTTP/1.0 200 Connection established\r\n" {
		t.Fatalf("status %q %v", line, err)
	}
	if line, err := br.ReadString('\n'); err != nil || line != "\r\n" {
		t.Fatalf("blank %q %v", line, err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("back %q %v", buf, err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not finish")
	}
	if toNet.String() != "ping" || fromNet.String() != "pong" {
		t.Fatalf("up %q down %q", toNet.String(), fromNet.String())
	}
}

type memClose struct{ *bytes.Buffer }

func (m memClose) Close() error { return nil }

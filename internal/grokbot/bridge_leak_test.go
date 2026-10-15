package grokbot

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// noHalfClose 模拟经 socks 拨出的连接：没有 CloseWrite，客户端的 EOF 传不到上游。
type noHalfClose struct{ c net.Conn }

func (n noHalfClose) Read(p []byte) (int, error)         { return n.c.Read(p) }
func (n noHalfClose) Write(p []byte) (int, error)        { return n.c.Write(p) }
func (n noHalfClose) Close() error                       { return n.c.Close() }
func (n noHalfClose) LocalAddr() net.Addr                { return n.c.LocalAddr() }
func (n noHalfClose) RemoteAddr() net.Addr               { return n.c.RemoteAddr() }
func (n noHalfClose) SetDeadline(t time.Time) error      { return n.c.SetDeadline(t) }
func (n noHalfClose) SetReadDeadline(t time.Time) error  { return n.c.SetReadDeadline(t) }
func (n noHalfClose) SetWriteDeadline(t time.Time) error { return n.c.SetWriteDeadline(t) }

// 客户端关掉 CONNECT 隧道后，即使上游一直不说话、也收不到半关闭，桥也必须释放两头连接。
func TestBridgeReleasesUpstreamAfterClientCloses(t *testing.T) {
	t.Cleanup(setPipeGrace(200 * time.Millisecond))

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	released := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(io.Discard, c) // 静默长连接：只有连接真正关掉才返回
		close(released)
	}()

	var b Bridge
	addr, err := b.Ensure("leak-test-"+ln.Addr().String(), func(ctx context.Context, network, a string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, network, a)
		if err != nil {
			return nil, err
		}
		return noHalfClose{c}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	c, err := net.Dial("tcp", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	target := ln.Addr().String()
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("connect: %v %v", resp, err)
	}
	_, _ = io.WriteString(c, "hello")
	c.Close()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端关闭后上游连接没有释放（CLOSE_WAIT 泄漏）")
	}
}

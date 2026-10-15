package grokbot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/connlog"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func captureLog(t *testing.T, level slog.Level) *syncBuf {
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	SetConnDebug(level <= slog.LevelDebug)
	t.Cleanup(func() { slog.SetDefault(old); SetConnDebug(false) })
	return buf
}

func logEchoServer(t *testing.T) net.Listener {
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
	return ln
}

func tunnel(t *testing.T, proxy, target string) {
	c, err := net.Dial("tcp4", strings.TrimPrefix(proxy, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\nProxy-Authorization: Basic c2VjcmV0\r\n\r\n")
	buf := make([]byte, 39)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "hello")
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, buf *syncBuf, sub string) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := buf.String(); strings.Contains(s, sub) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没等到 %q，日志：\n%s", sub, buf.String())
	return ""
}

func TestBridgeConnLogDebugOnly(t *testing.T) {
	ln := logEchoServer(t)
	dial := func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.Set(ctx, "proxy")
		var d net.Dialer
		return d.DialContext(ctx, n, a)
	}

	buf := captureLog(t, slog.LevelInfo)
	var b Bridge
	addr, err := b.Ensure("socks5://user:pw@127.0.0.1:1", dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	tunnel(t, addr, ln.Addr().String())
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(buf.String(), "bridge conn") {
		t.Fatalf("普通模式不应写连接日志：%s", buf.String())
	}

	buf = captureLog(t, slog.LevelDebug)
	tunnel(t, addr, ln.Addr().String())
	s := waitFor(t, buf, "bridge conn")
	for _, want := range []string{"bridge=grok", "id=g-", "target=" + ln.Addr().String(), "route=proxy/socks5", "up=5", "down=5", "closed_by=client", "dial_ms=", "dur_ms="} {
		if !strings.Contains(s, want) {
			t.Errorf("缺 %q：%s", want, s)
		}
	}
	for _, bad := range []string{"pw", "c2VjcmV0", "hello", "Proxy-Authorization"} {
		if strings.Contains(s, bad) {
			t.Errorf("日志不应含 %q：%s", bad, s)
		}
	}
	if n := strings.Count(strings.TrimSpace(s), "\n"); n != 0 {
		t.Errorf("一条连接应只有一行：%s", s)
	}
}

func TestBridgeDialFailureLoggedAndSummarized(t *testing.T) {
	buf := captureLog(t, slog.LevelDebug)
	var b Bridge
	addr, err := b.Ensure("codex\nhttp://u:secretpw@127.0.0.1:9", func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.Set(ctx, "proxy")
		return nil, &url.Error{Op: "Get", URL: "http://u:secretpw@127.0.0.1:9", Err: errors.New("connection refused")}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp4", strings.TrimPrefix(addr, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(c, "CONNECT a.example:443 HTTP/1.1\r\nHost: a.example:443\r\n\r\n")
		_, _ = io.ReadAll(c)
		c.Close()
	}
	waitFor(t, buf, "closed_by=dial")
	time.Sleep(50 * time.Millisecond)
	b.reportStats()
	s := buf.String()
	for _, want := range []string{"bridge=codex", "id=c-", "route=proxy/http", "dial_fail=3", "a.example:443×3", "connection refused", "active=0", "close_wait="} {
		if !strings.Contains(s, want) {
			t.Errorf("缺 %q：%s", want, s)
		}
	}
	if strings.Contains(s, "secretpw") {
		t.Errorf("日志泄露了密码：%s", s)
	}
}

func TestPipeReportsUpstreamFirstAndForced(t *testing.T) {
	t.Cleanup(setPipeGrace(100 * time.Millisecond))
	c1, c2 := net.Pipe()
	u1, u2 := net.Pipe()
	var b Bridge
	go func() { _, _ = u2.Write([]byte("abc")); u2.Close() }()
	go func() { buf := make([]byte, 3); _, _ = io.ReadFull(c2, buf) }() // 客户端读完不关：上游先断，收尾超时
	r := b.pipe(c1, u1)
	if r.first != "upstream" || !r.forced || r.toClient != 3 {
		t.Fatalf("得到 %+v", r)
	}
	c2.Close()
}

func TestSanitizeErrAndProxyKind(t *testing.T) {
	e := &url.Error{Op: "Get", URL: "http://a:b@h:1/x", Err: errors.New("boom")}
	if s := sanitizeErr(e); strings.Contains(s, "a:b") || !strings.Contains(s, "boom") {
		t.Fatal(s)
	}
	if s := sanitizeErr(errors.New("dial socks5://x:y@h:1 failed")); s != "dial socks5://***@h:1 failed" {
		t.Fatal(s)
	}
	for in, want := range map[string]string{"socks5://u:p@h:1": "socks5", "codex\nHTTP://h:1": "http", "h:1": "http"} {
		if got := proxyKind(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

func TestBridgeBadRequestLogsNoPeerText(t *testing.T) {
	buf := captureLog(t, slog.LevelDebug)
	var b Bridge
	addr, err := b.Ensure("bad-request-test", func(ctx context.Context, n, a string) (net.Conn, error) { return nil, errors.New("unused") })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	c, err := net.Dial("tcp4", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET http://h.example/p?token=SECRETTOKEN BROKEN\r\n\r\n")
	_, _ = io.ReadAll(c)
	c.Close()
	s := waitFor(t, buf, "bridge conn")
	if strings.Contains(s, "SECRETTOKEN") || strings.Contains(s, "token") {
		t.Fatalf("request text leaked into the log: %s", s)
	}
	if !strings.Contains(s, "bad request (malformed)") {
		t.Fatalf("want a fixed reason: %s", s)
	}
}

func TestBadRequestKind(t *testing.T) {
	if badRequestKind(io.EOF) != "eof" || badRequestKind(io.ErrUnexpectedEOF) != "eof" || badRequestKind(errors.New("x")) != "malformed" {
		t.Fatal("kinds")
	}
}

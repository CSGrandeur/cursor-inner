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
	if !strings.Contains(s, "_file_only=true") {
		t.Errorf("拨号失败告警应只写日志文件，避免裸刷控制台：%s", s)
	}
}

func TestBridgeDirectDialFailureNotMisreportedAsProxy(t *testing.T) {
	buf := captureLog(t, slog.LevelDebug)
	var b Bridge
	addr, err := b.Ensure("antigravity\nsocks5://127.0.0.1:1", func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.SetReason(ctx, "reserved_ip")
		connlog.Set(ctx, "direct")
		return nil, errors.New("i/o timeout")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	c, err := net.Dial("tcp4", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "CONNECT internal.example:8080 HTTP/1.1\r\nHost: internal.example:8080\r\n\r\n")
	_, _ = io.ReadAll(c)
	c.Close()
	s := waitFor(t, buf, "closed_by=dial")
	if strings.Contains(s, "没能经代理连上目标") {
		t.Fatalf("直连失败不应误报成代理连接失败：%s", s)
	}
	for _, want := range []string{"antigravity 代理桥直连目标失败", "route=direct", "route_why=reserved_ip", "_file_only=true"} {
		if !strings.Contains(s, want) {
			t.Errorf("缺 %q：%s", want, s)
		}
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

func TestBridgeNameAntigravity(t *testing.T) {
	var b Bridge
	b.key = "antigravity\nhttp://127.0.0.1:1"
	if b.name() != "antigravity" {
		t.Fatalf("got %q", b.name())
	}
	b.key = "codex\nhttp://127.0.0.1:1"
	if b.name() != "codex" {
		t.Fatalf("got %q", b.name())
	}
	b.key = "http://127.0.0.1:1"
	if b.name() != "grok" {
		t.Fatalf("got %q", b.name())
	}
}

func TestAntigravityFamily(t *testing.T) {
	if !antigravityFamily("oauth2.googleapis.com") || !antigravityFamily("antigravity-unleash.goog") || !antigravityFamily("antigravity.static.usercontent.goog") {
		t.Fatal("expected google official hosts")
	}
	if !antigravityFamily("policies.google.com") || !antigravityFamily("www.google.com") || !antigravityFamily("accounts.google.com") {
		t.Fatal("expected google.com family hosts")
	}
	if !antigravityFamily("antigravity.google") || !antigravityFamily("auth.cloud.google") || !antigravityFamily("ai.google.dev") || !antigravityFamily("csp.withgoogle.com") {
		t.Fatal("expected .google / google.dev / withgoogle.com family hosts")
	}
	if !antigravityFamily("stun.l.google.com") || !antigravityFamily("turn.goog") || !antigravityFamily("workspace.turns.goog") {
		t.Fatal("expected google webrtc stun/turn hosts")
	}
	if !antigravityFamily("antigravity-hub-auto-updater-1.us-central1.run.app") || !antigravityFamily("antigravity-ide-auto-updater-1.us-central1.run.app") {
		t.Fatal("expected cloud run updater")
	}
	if antigravityFamily("api2.cursor.sh") || antigravityFamily("api.x.ai") || antigravityFamily("example.com") {
		t.Fatal("non-antigravity hosts must stay out")
	}
}

func TestPipeIgnoresPostEOFWriteAbortButPreservesUpstreamReadReset(t *testing.T) {
	// 1. 客户端读完正常全关（EOF），随后上游补发 24 字节 TLS close_notify 触发向已关客户端写入报错：应归为正常 eof，不计 fail。
	postCloseLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { postCloseLn.Close() })
	go func() {
		for {
			s, err := postCloseLn.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				defer s.Close()
				req := make([]byte, 3)
				if _, err := io.ReadFull(s, req); err != nil {
					return
				}
				_, _ = s.Write([]byte("ok!"))
				// 等桥把客户端的 FIN 转过来后，模拟上游延迟发出收尾帧（如 TLS close_notify）。
				buf := make([]byte, 1)
				_, _ = s.Read(buf)
				time.Sleep(30 * time.Millisecond)
				_, _ = s.Write(bytes.Repeat([]byte("z"), 24))
			}(s)
		}
	}()

	// 2. 客户端半关闭等待响应，上游直接发 TCP RST：属于真实读取重置，必须归为 reset 并计入 fail。
	rstLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rstLn.Close() })
	go func() {
		for {
			s, err := rstLn.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				req := make([]byte, 3)
				_, _ = io.ReadFull(s, req)
				if tc, ok := s.(*net.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
				_ = s.Close()
			}(s)
		}
	}()

	buf := captureLog(t, slog.LevelDebug)
	SetConnDetail(true)
	oldProbe := probeTarget
	probeTarget = "off"
	t.Cleanup(func() { SetConnDetail(false); probeTarget = oldProbe })

	var b Bridge
	addr, err := b.Ensure("antigravity\nhttp://127.0.0.1:1080", func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.Set(ctx, "proxy")
		var d net.Dialer
		return d.DialContext(ctx, n, a)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	bridgeAddr := strings.TrimPrefix(addr, "http://")

	// 跑场景 1：客户端全关（SetLinger(0) 强制后续写客户端立即报 reset/broken pipe）。
	c1, err := net.Dial("tcp4", bridgeAddr)
	if err != nil {
		t.Fatal(err)
	}
	t1 := postCloseLn.Addr().String()
	_, _ = io.WriteString(c1, "CONNECT "+t1+" HTTP/1.1\r\nHost: "+t1+"\r\n\r\n")
	okHdr := make([]byte, 39)
	if _, err := io.ReadFull(c1, okHdr); err != nil {
		t.Fatal(err)
	}
	_, _ = c1.Write([]byte("req"))
	resp := make([]byte, 3)
	if _, err := io.ReadFull(c1, resp); err != nil {
		t.Fatal(err)
	}
	_ = c1.(*net.TCPConn).CloseWrite()
	time.Sleep(10 * time.Millisecond)
	_ = c1.(*net.TCPConn).SetLinger(0)
	_ = c1.Close()

	s1 := waitFor(t, buf, "id=a-1")
	if !strings.Contains(s1, "close=eof") {
		t.Fatalf("expected post-EOF trailing write error to classify as close=eof, got:\n%s", s1)
	}

	// 跑场景 2：上游真实 RST。
	c2, err := net.Dial("tcp4", bridgeAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	t2 := rstLn.Addr().String()
	_, _ = io.WriteString(c2, "CONNECT "+t2+" HTTP/1.1\r\nHost: "+t2+"\r\n\r\n")
	if _, err := io.ReadFull(c2, okHdr); err != nil {
		t.Fatal(err)
	}
	_, _ = c2.Write([]byte("req"))
	_ = c2.(*net.TCPConn).CloseWrite()
	_, _ = io.ReadAll(c2)

	s2 := waitFor(t, buf, "id=a-2")
	if !strings.Contains(s2, "close=reset") {
		t.Fatalf("expected genuine upstream RST to classify as close=reset, got:\n%s", s2)
	}
}

func TestBridgeStatsLiveBytesAndProgress(t *testing.T) {
	const payloadSize = 11 << 20
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			s, err := ln.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				defer s.Close()
				chunk := bytes.Repeat([]byte("a"), 64<<10)
				for rem := payloadSize; rem > 0; {
					n := len(chunk)
					if rem < n {
						n = rem
					}
					if _, err := s.Write(chunk[:n]); err != nil {
						return
					}
					rem -= n
				}
				<-hold
			}(s)
		}
	}()

	buf := captureLog(t, slog.LevelDebug)
	var b Bridge
	addr, err := b.Ensure("antigravity\nsocks5://127.0.0.1:1080", func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.Set(ctx, "proxy")
		var d net.Dialer
		return d.DialContext(ctx, n, a)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)

	c, err := net.Dial("tcp4", strings.TrimPrefix(addr, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := ln.Addr().String()
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	okHdr := make([]byte, 39)
	if _, err := io.ReadFull(c, okHdr); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, c, payloadSize); err != nil {
		t.Fatal(err)
	}

	s := waitFor(t, buf, `msg="bridge progress"`)
	if strings.Contains(s, `msg="bridge conn"`) {
		t.Fatalf("bridge conn must not be logged while tunnel is still open:\n%s", s)
	}
	for _, want := range []string{"bridge=antigravity", "target=" + target, "route=proxy/socks5", "down="} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in progress log:\n%s", want, s)
		}
	}

	b.reportStats()
	statsLog := waitFor(t, buf, `msg="bridge stats"`)
	for _, want := range []string{"bridge=antigravity", "up_bytes=0", "down_bytes=11534336"} {
		if !strings.Contains(statsLog, want) {
			t.Fatalf("missing %q in live bridge stats:\n%s", want, statsLog)
		}
	}
}

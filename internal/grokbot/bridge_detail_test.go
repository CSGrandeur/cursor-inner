package grokbot

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/connlog"
)

func TestCloseClass(t *testing.T) {
	cases := []struct{ by, reason, want string }{
		{"client", "", "eof"},
		{"upstream", "", "eof"},
		{"client+grace_timeout", "", "grace"},
		{"upstream", "read tcp: connection reset by peer", "reset"},
		{"upstream", "wsarecv: An existing connection was forcibly closed by the remote host.", "reset"},
		{"upstream", "i/o timeout", "timeout"},
		{"dial", "dial tcp 127.0.0.1:1080: connect: connection refused", "dial_refused"},
		{"dial", "context deadline exceeded", "dial_timeout"},
		{"dial", "socks5: general failure", "dial_fail"},
		{"client", "bad request (eof)", "error"},
	}
	for _, c := range cases {
		if got := closeClass(c.by, c.reason); got != c.want {
			t.Errorf("closeClass(%q,%q)=%q want %q", c.by, c.reason, got, c.want)
		}
	}
}

func TestHostOfCollapsesPods(t *testing.T) {
	if h := hostOf("9db1-pod-x-1340.us10.cursorvm.com:443"); h != "*.cursorvm.com" {
		t.Fatal(h)
	}
	if h := hostOf("API2.cursor.sh:443"); h != "api2.cursor.sh" {
		t.Fatal(h)
	}
}

func TestParseStatus(t *testing.T) {
	if parseStatus([]byte("HTTP/1.1 503")) != 503 || parseStatus([]byte("\x16\x03\x01abcdefghi")) != 0 || parseStatus([]byte("HTTP/1.1")) != 0 {
		t.Fatal("parseStatus")
	}
}

func TestHostLinesOnlySlowOrFailed(t *testing.T) {
	var s hostStats
	s.add("a.example", time.Millisecond, 10*time.Millisecond, "eof")
	s.add("b.example", time.Millisecond, slowThreshold+time.Second, "eof")
	s.add("c.example", time.Millisecond, 0, "reset")
	s.add("c.example", time.Millisecond, 0, "timeout")
	lines := hostLines(s.take())
	if len(lines) != 3 {
		t.Fatalf("want 2 problem hosts + healthy line, got %v", lines)
	}
	joined := ""
	for _, l := range lines {
		joined += strings.Trim(strings.ReplaceAll(strings.ReplaceAll(fmtAny(l), "[", ""), "]", ""), " ") + "\n"
	}
	for _, want := range []string{"b.example", "c.example", "reset×1,timeout×1", "(healthy)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %q：%s", want, joined)
		}
	}
	if strings.Contains(joined, "a.example") {
		t.Errorf("健康主机不应单独成行：%s", joined)
	}
	if s.take() != nil {
		t.Fatal("take 应清零")
	}
}

func fmtAny(a []any) string {
	var b strings.Builder
	for _, v := range a {
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(toS(v), "\n", " "), "\t", " ")))
		b.WriteByte(' ')
	}
	return b.String()
}

func toS(v any) string { return slog.AnyValue(v).String() }

func TestProxyHostDropsCredentials(t *testing.T) {
	if h := proxyHost("socks5://u:pw@127.0.0.1:1080"); h != "127.0.0.1:1080" {
		t.Fatal(h)
	}
	if h := proxyHost("codex\nhttp://127.0.0.1:8080"); h != "127.0.0.1:8080" {
		t.Fatal(h)
	}
}

func TestRunProbeMeasuresProxyAndTLS(t *testing.T) {
	ln := logEchoServer(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "https://")
	dial := func(ctx context.Context, n, a string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, n, a)
	}
	r := runProbe(ln.Addr().String(), target, dial)
	if r.proxyTCP < 0 || r.proxyErr != "" {
		t.Fatalf("proxy tcp: %+v", r)
	}
	if r.viaDial < 0 {
		t.Fatalf("via dial: %+v", r)
	}
	// 测试证书不受信：握手失败也要归类，不带对端文本。
	if r.viaTLS >= 0 || !strings.HasPrefix(r.viaErr, "tls_") {
		t.Fatalf("via tls: %+v", r)
	}
	r = runProbe("127.0.0.1:1", "", nil)
	if r.proxyErr == "" || r.proxyTCP >= 0 {
		t.Fatalf("refused proxy: %+v", r)
	}
}

func TestBridgeDetailLogOffByDefault(t *testing.T) {
	ln := logEchoServer(t)
	dial := func(ctx context.Context, n, a string) (net.Conn, error) {
		connlog.Set(ctx, "proxy")
		var d net.Dialer
		return d.DialContext(ctx, n, a)
	}
	// 只开基础调试（发版 --debug）：不应出现详细字段。
	buf := captureLog(t, slog.LevelDebug)
	var b Bridge
	addr, err := b.Ensure("socks5://user:pw@127.0.0.1:1", dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	tunnel(t, addr, ln.Addr().String())
	s := waitFor(t, buf, "bridge conn")
	if strings.Contains(s, "ttfb_ms") || strings.Contains(s, "close=") {
		t.Fatalf("发版 --debug 不应带详细字段：%s", s)
	}

	buf = captureLog(t, slog.LevelDebug)
	SetConnDetail(true)
	oldProbe := probeTarget
	probeTarget = "off"
	t.Cleanup(func() { SetConnDetail(false); probeTarget = oldProbe })
	tunnel(t, addr, ln.Addr().String())
	s = waitFor(t, buf, "bridge conn")
	for _, want := range []string{"ttfb_ms=", "close=eof", "start="} {
		if !strings.Contains(s, want) {
			t.Errorf("缺 %q：%s", want, s)
		}
	}
	if strings.Contains(s, "ttfb_ms=-1") {
		t.Errorf("回显服务器回了数据，首字节应测到：%s", s)
	}
	for _, bad := range []string{"pw", "c2VjcmV0", "hello"} {
		if strings.Contains(s, bad) {
			t.Errorf("日志不应含 %q：%s", bad, s)
		}
	}
	b.reportStats()
	// 健康主机只合成一行。
	s = waitFor(t, buf, "bridge hosts")
	if !strings.Contains(s, "(healthy)") {
		t.Errorf("汇总：%s", s)
	}
}

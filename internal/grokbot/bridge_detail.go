package grokbot

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-inner/internal/connlog"
)

// 连接详细日志：只有个人记录版调用 SetConnDetail 打开，发版即使带 --debug 也保持原来的行数与字段。
// 打开后在 “bridge conn” 行上多记开始时间、首字节耗时、HTTP 状态（明文请求才看得到）、断开归类，
// 每分钟按主机汇总慢请求与失败（“bridge hosts”），并探测一次代理本身和经代理到官方主机的耗时（“bridge probe”）。
// 只记主机:端口、耗时、字节数和归类后的原因，不记请求头、Cookie、令牌、认证信息和内容。

var connDetail atomic.Bool

// SetConnDetail 打开或关闭连接详细日志。它同时打开 SetConnDebug 的基础连接日志。
func SetConnDetail(on bool) {
	connDetail.Store(on)
	if on {
		SetConnDebug(true)
	}
}

func detailOn() bool { return connDetail.Load() && debugOn() }

// slowThreshold 之上的首字节或拨号耗时算慢；CURSOR_INNER_CONN_SLOW_MS 可改。
var slowThreshold = func() time.Duration {
	if v := os.Getenv("CURSOR_INNER_CONN_SLOW_MS"); v != "" {
		var ms int
		if _, err := fmt.Sscan(v, &ms); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 3 * time.Second
}()

// probeTarget 是经代理探测的官方主机；CURSOR_INNER_PROBE_TARGET 可改，设为 off 不探测。
var probeTarget = func() string {
	if v := strings.TrimSpace(os.Getenv("CURSOR_INNER_PROBE_TARGET")); v != "" {
		return v
	}
	return "api.x.ai:443"
}()

// firstByteConn 记下上游第一次读到数据的时间，其余原样转发（含 CloseWrite）。
type firstByteConn struct {
	net.Conn
	first atomic.Int64 // UnixNano，0 表示还没收到
}

func (c *firstByteConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.first.Load() == 0 {
		c.first.CompareAndSwap(0, time.Now().UnixNano())
	}
	return n, err
}

func (c *firstByteConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// firstAt 返回首字节时间；没收到时为零值。
func (c *firstByteConn) firstAt() time.Time {
	if v := c.first.Load(); v != 0 {
		return time.Unix(0, v)
	}
	return time.Time{}
}

// closeClass 把一条连接的结束归成固定几类：ok / eof / reset / timeout / refused / grace / dial_fail / error。
func closeClass(closedBy, reason string) string {
	r := strings.ToLower(reason)
	switch {
	case closedBy == "dial":
		switch {
		case strings.Contains(r, "timeout"), strings.Contains(r, "deadline"):
			return "dial_timeout"
		case strings.Contains(r, "refused"):
			return "dial_refused"
		}
		return "dial_fail"
	case strings.Contains(r, "reset"), strings.Contains(r, "forcibly closed"), strings.Contains(r, "broken pipe"), strings.Contains(r, "aborted"):
		return "reset"
	case strings.Contains(r, "timeout"), strings.Contains(r, "deadline"):
		return "timeout"
	case strings.Contains(r, "refused"):
		return "refused"
	case strings.Contains(closedBy, "grace_timeout"):
		return "grace"
	case reason == "" && (strings.HasPrefix(closedBy, "client") || strings.HasPrefix(closedBy, "upstream")):
		return "eof"
	case reason == "":
		return "ok"
	}
	return "error"
}

func failedClass(c string) bool {
	switch c {
	case "reset", "timeout", "refused", "dial_fail", "dial_timeout", "dial_refused", "error":
		return true
	}
	return false
}

// hostOf 去掉端口，只留主机名；cursorvm 的动态子域并成 *.cursorvm.com，避免汇总被几千个 pod 名刷屏。
func hostOf(target string) string {
	h := target
	if hh, _, err := net.SplitHostPort(target); err == nil {
		h = hh
	}
	h = strings.ToLower(h)
	for _, suf := range []string{".cursorvm.com", ".cursor-cdn.com"} {
		if strings.HasSuffix(h, suf) && strings.Count(h, ".") > 2 {
			return "*" + suf
		}
	}
	return h
}

type hostAgg struct {
	n, fail, slow   int
	ttfbSum, ttfbN  int64
	ttfbMax, dialMx time.Duration
	classes         map[string]int
}

type hostStats struct {
	mu sync.Mutex
	m  map[string]*hostAgg
}

func (s *hostStats) add(host string, dial, ttfb time.Duration, class string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*hostAgg{}
	}
	a := s.m[host]
	if a == nil {
		if len(s.m) >= 256 {
			host = "(other)"
			if a = s.m[host]; a == nil {
				a = &hostAgg{}
				s.m[host] = a
			}
		} else {
			a = &hostAgg{}
			s.m[host] = a
		}
	}
	a.n++
	if dial > a.dialMx {
		a.dialMx = dial
	}
	if ttfb > 0 {
		a.ttfbSum += ttfb.Milliseconds()
		a.ttfbN++
		if ttfb > a.ttfbMax {
			a.ttfbMax = ttfb
		}
	}
	if failedClass(class) {
		a.fail++
		if a.classes == nil {
			a.classes = map[string]int{}
		}
		a.classes[class]++
	}
	if dial >= slowThreshold || ttfb >= slowThreshold {
		a.slow++
	}
}

func (s *hostStats) take() map[string]*hostAgg {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.m
	s.m = nil
	return m
}

// hostLines 生成每分钟的主机汇总：有慢或失败的主机各一行，其余合成一行。
func hostLines(m map[string]*hostAgg) [][]any {
	if len(m) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var out [][]any
	okHosts, okConns := 0, 0
	for _, h := range hosts {
		a := m[h]
		if a.fail == 0 && a.slow == 0 {
			okHosts++
			okConns += a.n
			continue
		}
		avg := int64(0)
		if a.ttfbN > 0 {
			avg = a.ttfbSum / a.ttfbN
		}
		attrs := []any{"host", h, "conns", a.n, "slow", a.slow, "fail", a.fail,
			"ttfb_avg_ms", avg, "ttfb_max_ms", a.ttfbMax.Milliseconds(), "dial_max_ms", a.dialMx.Milliseconds()}
		if len(a.classes) > 0 {
			ks := make([]string, 0, len(a.classes))
			for k, v := range a.classes {
				ks = append(ks, fmt.Sprintf("%s×%d", k, v))
			}
			sort.Strings(ks)
			attrs = append(attrs, "fail_kinds", strings.Join(ks, ","))
		}
		out = append(out, attrs)
	}
	out = append(out, []any{"host", "(healthy)", "hosts", okHosts, "conns", okConns})
	return out
}

func (b *Bridge) reportDetail() {
	m := b.hosts.take()
	if !detailOn() {
		return
	}
	for _, attrs := range hostLines(m) {
		slog.Debug("bridge hosts", append([]any{"bridge", b.name(), "slow_ms", slowThreshold.Milliseconds()}, attrs...)...)
	}
	if b.name() == "grok" && probeTarget != "off" && b.probing.CompareAndSwap(false, true) {
		go func() {
			defer b.probing.Store(false)
			b.probe()
		}()
	}
}

// proxyHost 从桥的 key 里取上游代理的 host:port，不带账号密码。
func proxyHost(key string) string {
	if i := strings.LastIndexByte(key, '\n'); i >= 0 {
		key = key[i+1:]
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if !strings.Contains(key, "://") {
		key = "http://" + key
	}
	u, err := url.Parse(key)
	if err != nil {
		return ""
	}
	return u.Host
}

// probeResult 是一次探测；字段为 -1 表示没测到。
type probeResult struct {
	proxyTCP, viaDial, viaTLS time.Duration
	proxyErr, viaErr          string
}

func (p probeResult) attrs() []any {
	ms := func(d time.Duration) int64 {
		if d < 0 {
			return -1
		}
		return d.Milliseconds()
	}
	a := []any{"proxy_tcp_ms", ms(p.proxyTCP), "via_dial_ms", ms(p.viaDial), "via_tls_ms", ms(p.viaTLS)}
	if p.proxyErr != "" {
		a = append(a, "proxy_error", p.proxyErr)
	}
	if p.viaErr != "" {
		a = append(a, "via_error", p.viaErr)
	}
	return a
}

// probeTimeout 是每一步的上限。
var probeTimeout = 10 * time.Second

func runProbe(proxyAddr, target string, dial DialFunc) probeResult {
	res := probeResult{proxyTCP: -1, viaDial: -1, viaTLS: -1}
	if proxyAddr != "" {
		t0 := time.Now()
		c, err := net.DialTimeout("tcp", proxyAddr, probeTimeout)
		if err != nil {
			res.proxyErr = closeClass("dial", sanitizeErr(err))
		} else {
			res.proxyTCP = time.Since(t0)
			_ = c.Close()
		}
	}
	if dial == nil || target == "" {
		return res
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	ctx, _ = connlog.With(ctx)
	t0 := time.Now()
	up, err := dial(ctx, "tcp", target)
	if err != nil {
		res.viaErr = closeClass("dial", sanitizeErr(err))
		return res
	}
	defer up.Close()
	res.viaDial = time.Since(t0)
	host := hostOf(target)
	_ = up.SetDeadline(time.Now().Add(probeTimeout))
	t1 := time.Now()
	tc := tls.Client(up, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			res.viaErr = "tls_timeout"
		} else {
			res.viaErr = "tls_" + closeClass("", sanitizeErr(err))
		}
		return res
	}
	res.viaTLS = time.Since(t1)
	_ = tc.Close()
	return res
}

func (b *Bridge) probe() {
	dial, key := b.currentDial()
	r := runProbe(proxyHost(key), probeTarget, dial)
	attrs := append([]any{"bridge", b.name(), "proxy", proxyHost(key), "target", probeTarget}, r.attrs()...)
	slog.Debug("bridge probe", attrs...)
}

// ttfbMS 是首字节耗时；隧道建好但上游一字节都没回时记 -1（典型的“连上了却卡住”）。
func ttfbMS(r *connRec, ttfb time.Duration) int64 {
	if r.tunnelAt.IsZero() {
		return -1
	}
	if r.firstAt.IsZero() {
		return -1
	}
	return ttfb.Milliseconds()
}

// parseStatus 从 “HTTP/1.1 200” 里取状态码，不是 HTTP 响应时返回 0。
func parseStatus(line []byte) int {
	s := string(line)
	if !strings.HasPrefix(s, "HTTP/1.") || len(s) < 12 || s[8] != ' ' {
		return 0
	}
	n := 0
	for _, ch := range s[9:12] {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

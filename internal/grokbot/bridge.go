package grokbot

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-inner/internal/connlog"
	"cursor-inner/internal/official"
	"cursor-inner/internal/selfupdate"
)

// DialFunc 与 dialer.Func 同形，避免 grokbot 依赖 dialer。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// NeedsBridge 报告这个代理地址 Grok Bot 自己用不了：Chromium 和 undici 都不认
// --proxy-server / HTTPS_PROXY 里的 user:pass，也不会对 socks 做 HTTP CONNECT。
// 这时由 cursor-inner 在回环地址上开一个 HTTP CONNECT 桥，出站经配置的代理（含认证）拨号。
func NeedsBridge(spec string) bool {
	spec = strings.TrimSpace(spec)
	if !strings.Contains(spec, "://") {
		spec = "http://" + spec
	}
	u, err := url.Parse(spec)
	if err != nil {
		return false
	}
	return u.User != nil || strings.HasPrefix(strings.ToLower(u.Scheme), "socks")
}

// Bridge 是只监听 127.0.0.1 的 HTTP 代理。所有出站都走 dial；dial 失败就回 502，绝不直连。
// 端口按代理地址算出一个固定值优先绑定，cursor-inner 重启后 Grok Bot 的启动参数仍然有效。
type Bridge struct {
	mu   sync.Mutex
	ln   net.Listener
	dial DialFunc
	key  string

	released string
	stats    bridgeStats
	hosts    hostStats
	probing  atomic.Bool
}

var (
	bridgePortsMu     sync.Mutex
	activeBridgePorts = map[int]int{}
)

func registerBridgePort(port int) {
	if port <= 0 {
		return
	}
	bridgePortsMu.Lock()
	activeBridgePorts[port]++
	bridgePortsMu.Unlock()
}

func unregisterBridgePort(port int) {
	if port <= 0 {
		return
	}
	bridgePortsMu.Lock()
	if n := activeBridgePorts[port]; n <= 1 {
		delete(activeBridgePorts, port)
	} else {
		activeBridgePorts[port] = n - 1
	}
	bridgePortsMu.Unlock()
}

func isActiveBridgePort(port int) bool {
	if port <= 0 {
		return false
	}
	bridgePortsMu.Lock()
	n := activeBridgePorts[port]
	bridgePortsMu.Unlock()
	return n > 0
}

func addrPort(addr net.Addr) int {
	ta, ok := addr.(*net.TCPAddr)
	if !ok || ta == nil {
		return 0
	}
	return ta.Port
}

func rebindLoopback(port int, wait time.Duration) (net.Listener, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var ln net.Listener
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	err := selfupdate.Rebind(ctx, func() error {
		l, lerr := net.Listen("tcp4", addr)
		if lerr == nil {
			ln = l
		}
		return lerr
	})
	return ln, err
}

func portListening(port int) bool {
	if port <= 0 {
		return false
	}
	c, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Ensure 让桥按 key（代理地址）和 dial 运行，返回给 Grok Bot 用的 http://127.0.0.1:端口。
func (b *Bridge) Ensure(key string, dial DialFunc) (string, error) {
	u, _, err := b.EnsureAt(key, 0, dial)
	return u, err
}

// EnsureAt 让桥按 key 和 dial 运行；preferredPort > 0 时优先复用或抢占该回环端口。
// 若 preferredPort 仍在被其他进程（如正在排空的旧实例）服务，返回 preferredBusy=true 且不改现有监听，
// 让调用方保持现状，等旧进程释放后再原地接手。
func (b *Bridge) EnsureAt(key string, preferredPort int, dial DialFunc) (url string, preferredBusy bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dial = dial
	if b.ln != nil && b.key == key {
		if preferredPort <= 0 || addrPort(b.ln.Addr()) == preferredPort {
			return "http://" + b.ln.Addr().String(), false, nil
		}
	}
	if preferredPort > 0 {
		if ln, rerr := rebindLoopback(preferredPort, 300*time.Millisecond); rerr == nil {
			b.swapListenerLocked(ln, key)
			return "http://" + ln.Addr().String(), false, nil
		}
		if portListening(preferredPort) {
			return "", true, nil
		}
	}
	if b.ln != nil {
		_ = b.ln.Close()
		b.ln = nil
	}
	ln, err := rebindLoopback(stablePort(key), 300*time.Millisecond)
	if err != nil {
		ln, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", false, err
		}
	}
	b.swapListenerLocked(ln, key)
	return "http://" + ln.Addr().String(), false, nil
}

func (b *Bridge) swapListenerLocked(ln net.Listener, key string) {
	if b.ln != nil {
		_ = b.ln.Close()
	}
	b.ln, b.key = ln, key
	registerBridgePort(addrPort(ln.Addr()))
	go b.serve(ln)
}

// Retarget 只换桥的出站拨号，不动监听。Grok Bot 改用别的代理地址、但还没重开时，
// 仍连着桥的请求也按当前设置出站。
func (b *Bridge) Retarget(dial DialFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln != nil {
		b.dial = dial
	}
}

// Close 停掉桥。之后 Grok Bot 连不上代理，但不会改成直连。
func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln != nil {
		_ = b.ln.Close()
		b.ln = nil
	}
}

// Release 停止接受新连接（已建立的连接继续），记住地址供 Reclaim。自更新交接用。
func (b *Bridge) Release() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln == nil {
		return nil
	}
	b.released = b.ln.Addr().String()
	err := b.ln.Close()
	b.ln = nil
	return err
}

// Reclaim 在交出的地址上重新监听（交接失败回滚）。
func (b *Bridge) Reclaim() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released == "" || b.ln != nil {
		return nil
	}
	addr := b.released
	var ln net.Listener
	err := selfupdate.Rebind(context.Background(), func() error {
		l, err := net.Listen("tcp4", addr)
		if err == nil {
			ln = l
		}
		return err
	})
	if err != nil {
		return err
	}
	b.ln, b.released = ln, ""
	registerBridgePort(addrPort(ln.Addr()))
	go b.serve(ln)
	return nil
}

func stablePort(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte("cursor-inner-grok-bridge\n" + key))
	return 20000 + int(h.Sum32()%20000)
}

func (b *Bridge) serve(ln net.Listener) {
	defer unregisterBridgePort(addrPort(ln.Addr()))
	b.startReporter()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go b.handle(c)
	}
}

func (b *Bridge) listenPort() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln == nil {
		return 0
	}
	return addrPort(b.ln.Addr())
}

func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified()
}

func (b *Bridge) isSelfTarget(target string) bool {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || !isLoopbackHost(host) {
		return false
	}
	if lp := b.listenPort(); lp > 0 && lp == port {
		return true
	}
	return isActiveBridgePort(port)
}

func (b *Bridge) currentDial() (DialFunc, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dial, b.key
}

// connRec 是一条桥连接的调试记录，结束时写成一行。
type connRec struct {
	id, client, target, targetSrc, method, kind, closedBy, reason string
	note                                                          *connlog.Note
	start                                                         time.Time
	dial                                                          time.Duration
	toUp, toClient                                                int64
	tunnelAt, firstAt                                             time.Time // 隧道建立、上游首字节（详细日志）
	status                                                        int       // 明文 HTTP 的响应状态（详细日志）
}

func (b *Bridge) handle(c net.Conn) {
	defer c.Close()
	if tcp, ok := c.RemoteAddr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		return
	}
	b.stats.active.Add(1)
	b.stats.opened.Add(1)
	defer b.stats.active.Add(-1)
	defer b.stats.closed.Add(1)
	rec := &connRec{client: c.RemoteAddr().String(), start: time.Now()}
	defer b.logConn(rec)

	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		// 不记 err 文本：http.ReadRequest 的 malformed 错误会带上整行请求（含 URL 和查询串里的令牌）。
		rec.closedBy, rec.reason = "client", "bad request ("+badRequestKind(err)+")"
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	dial, key := b.currentDial()
	rec.kind = proxyKind(key)
	if dial == nil {
		rec.status = http.StatusBadGateway
		rec.closedBy, rec.reason = "bridge", "no dialer"
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target, src, ok := proxyDialTarget(req)
	rec.method = safeHTTPMethod(req.Method)
	rec.targetSrc = src
	rec.target = target
	if !ok {
		rec.status = http.StatusBadRequest
		rec.closedBy, rec.reason = "client", "empty target"
		if src != "" && src != "empty" {
			rec.reason = "empty target (" + src + ")"
		}
		_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if b.isSelfTarget(target) {
		rec.status = http.StatusBadRequest
		rec.closedBy, rec.reason = "client", "self target"
		_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ctx, rec.note = connlog.With(ctx)
	t0 := time.Now()
	up, err := dial(ctx, "tcp", target)
	rec.dial = time.Since(t0)
	cancel()
	if err != nil {
		b.stats.dialFailed(target, err)
		rec.status = http.StatusBadGateway
		rec.closedBy, rec.reason = "dial", sanitizeErr(err)
		slog.Warn(b.name()+" 代理桥没能经代理连上目标，已拒绝（不会直连）", "target", target, "error", sanitizeErr(err))
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	if req.Method == http.MethodConnect {
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			rec.closedBy, rec.reason = "client", sanitizeErr(err)
			return
		}
		rec.status = http.StatusOK
		if n := br.Buffered(); n > 0 {
			rest, _ := br.Peek(n)
			if _, err := up.Write(rest); err != nil {
				rec.closedBy, rec.reason = "upstream", sanitizeErr(err)
				return
			}
			rec.toUp += int64(n)
		}
		rec.tunnelAt = time.Now()
		fb := &firstByteConn{Conn: up}
		r := b.pipe(c, fb)
		rec.firstAt = fb.firstAt()
		rec.toUp += r.toUp
		rec.toClient = r.toClient
		rec.closedBy = r.first
		if r.forced {
			rec.closedBy += "+grace_timeout"
		}
		rec.reason = sanitizeErr(r.err)
		return
	}
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.Close = true
	if err := req.Write(up); err != nil {
		rec.closedBy, rec.reason = "upstream", sanitizeErr(err)
		return
	}
	rec.tunnelAt = time.Now()
	ubr := bufio.NewReader(up)
	if line, _ := ubr.Peek(12); len(line) > 0 {
		rec.firstAt = time.Now()
		rec.status = parseStatus(line)
	}
	n, err := io.Copy(c, ubr)
	rec.toClient = n
	rec.closedBy, rec.reason = "upstream", sanitizeErr(ioErr(err))
}

func (b *Bridge) logConn(r *connRec) {
	if !debugOn() {
		return
	}
	r.id = fmt.Sprintf("%s-%d", b.name()[:1], b.stats.seq.Add(1))
	route := r.note.Route()
	if route == "" {
		route = "none"
	}
	if route == "proxy" {
		route += "/" + r.kind
	}
	attrs := []any{"bridge", b.name(), "id", r.id, "client", r.client, "target", r.target, "route", route,
		"dial_ms", r.dial.Milliseconds(), "dur_ms", time.Since(r.start).Milliseconds(),
		"up", r.toUp, "down", r.toClient, "closed_by", r.closedBy,
		"method", r.method, "target_src", r.targetSrc, "target_empty", hostOf(r.target) == ""}
	if r.reason != "" {
		attrs = append(attrs, "reason", r.reason)
	}
	if why := r.note.Reason(); why != "" {
		attrs = append(attrs, "route_why", why)
	}
	if r.status > 0 {
		attrs = append(attrs, "status", r.status)
	}
	if host := hostOf(r.target); host != "" {
		if official.MustProxy(host) {
			attrs = append(attrs, "official", true)
		}
		if antigravityFamily(host) {
			attrs = append(attrs, "family", "antigravity")
		}
	}
	if detailOn() {
		var ttfb time.Duration
		if !r.firstAt.IsZero() && !r.tunnelAt.IsZero() {
			ttfb = r.firstAt.Sub(r.tunnelAt)
		}
		class := closeClass(r.closedBy, r.reason)
		attrs = append(attrs, "start", r.start.Format("15:04:05.000"), "ttfb_ms", ttfbMS(r, ttfb), "close", class)
		if r.target != "" {
			b.hosts.add(hostOf(r.target), r.dial, ttfb, class)
		}
	}
	slog.Debug("bridge conn", attrs...)
}

// pipeGrace 是一个方向结束后等另一方向收尾的时间。到点仍没结束就强制关掉两头：
// 经 socks 拨出的上游没有 CloseWrite，客户端的 EOF 传不过去，静默长连接会永远挂着
// （客户端侧堆积 CLOSE_WAIT，上游侧经代理的连接一直不释放）。
var pipeGraceNS atomic.Int64

func init() { pipeGraceNS.Store(int64(10 * time.Second)) }

func pipeGrace() time.Duration { return time.Duration(pipeGraceNS.Load()) }

// setPipeGrace 供测试改收尾时限，返回恢复函数。
func setPipeGrace(d time.Duration) func() {
	old := pipeGraceNS.Swap(int64(d))
	return func() { pipeGraceNS.Store(old) }
}

// pipe 双向转发 client 与 up。一方结束后最多再等 pipeGrace，然后关两头。
func (br *Bridge) pipe(client, up net.Conn) pipeResult {
	type side struct {
		name string
		n    int64
		err  error
	}
	done := make(chan side, 2)
	cp := func(name string, dst, src net.Conn) {
		n, err := io.Copy(dst, src)
		if tcp, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tcp.CloseWrite()
		}
		done <- side{name, n, ioErr(err)}
	}
	go cp("upstream", client, up) // 上游 → 客户端，结束说明上游先断
	go cp("client", up, client)
	var res pipeResult
	record := func(s side) {
		if s.name == "upstream" {
			res.toClient = s.n
		} else {
			res.toUp = s.n
		}
		if res.err == nil {
			res.err = s.err
		}
	}
	first := <-done
	res.first = first.name
	record(first)
	br.stats.grace.Add(1)
	select {
	case s := <-done:
		record(s)
	case <-time.After(pipeGrace()):
		res.forced = true
		br.stats.forced.Add(1)
	}
	br.stats.grace.Add(-1)
	_ = client.Close()
	_ = up.Close()
	if res.forced {
		// 关掉之后另一方向很快返回，补上它的字节数。
		select {
		case s := <-done:
			s.err = nil
			record(s)
		case <-time.After(time.Second):
		}
	}
	return res
}

// antigravityFamily 标出反重力相关官方主机，方便在混杂的 bridge conn 里过滤。
// 只根据主机名判断，不读请求内容。
func antigravityFamily(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || !official.Name(h) {
		return false
	}
	switch {
	case strings.HasSuffix(h, ".googleapis.com"), h == "googleapis.com",
		strings.HasSuffix(h, ".googleusercontent.com"), h == "googleusercontent.com",
		strings.HasSuffix(h, ".gstatic.com"), h == "gstatic.com",
		strings.HasSuffix(h, ".google.com"), h == "google.com",
		strings.HasSuffix(h, ".antigravity.google"), h == "antigravity.google",
		strings.HasSuffix(h, ".goog"), h == "goog",
		strings.HasSuffix(h, ".run.app") && strings.HasPrefix(strings.SplitN(h, ".", 2)[0], "antigravity-"):
		return true
	}
	return false
}

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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor-inner/internal/connlog"
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
}

// Ensure 让桥按 key（代理地址）和 dial 运行，返回给 Grok Bot 用的 http://127.0.0.1:端口。
func (b *Bridge) Ensure(key string, dial DialFunc) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dial = dial
	if b.ln != nil && b.key == key {
		return "http://" + b.ln.Addr().String(), nil
	}
	if b.ln != nil {
		_ = b.ln.Close()
		b.ln = nil
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", stablePort(key)))
	if err != nil {
		ln, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
	}
	b.ln, b.key = ln, key
	go b.serve(ln)
	return "http://" + ln.Addr().String(), nil
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
	go b.serve(ln)
	return nil
}

func stablePort(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte("cursor-inner-grok-bridge\n" + key))
	return 20000 + int(h.Sum32()%20000)
}

func (b *Bridge) serve(ln net.Listener) {
	b.startReporter()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go b.handle(c)
	}
}

func (b *Bridge) currentDial() (DialFunc, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dial, b.key
}

// connRec 是一条桥连接的调试记录，结束时写成一行。
type connRec struct {
	id, client, target, kind, closedBy, reason string
	note                                       *connlog.Note
	start                                      time.Time
	dial                                       time.Duration
	toUp, toClient                             int64
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
		rec.closedBy, rec.reason = "bridge", "no dialer"
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := req.Host
	if req.Method != http.MethodConnect {
		target = req.URL.Host
		if req.URL.Port() == "" {
			target = net.JoinHostPort(req.URL.Hostname(), "80")
		}
	}
	rec.target = target
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ctx, rec.note = connlog.With(ctx)
	t0 := time.Now()
	up, err := dial(ctx, "tcp", target)
	rec.dial = time.Since(t0)
	cancel()
	if err != nil {
		b.stats.dialFailed(target, err)
		rec.closedBy, rec.reason = "dial", sanitizeErr(err)
		slog.Warn("Grok 代理桥没能经代理连上目标，已拒绝（不会直连）", "target", target, "error", sanitizeErr(err))
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	if req.Method == http.MethodConnect {
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			rec.closedBy, rec.reason = "client", sanitizeErr(err)
			return
		}
		if n := br.Buffered(); n > 0 {
			rest, _ := br.Peek(n)
			if _, err := up.Write(rest); err != nil {
				rec.closedBy, rec.reason = "upstream", sanitizeErr(err)
				return
			}
			rec.toUp += int64(n)
		}
		r := b.pipe(c, up)
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
	n, err := io.Copy(c, up)
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
		"up", r.toUp, "down", r.toClient, "closed_by", r.closedBy}
	if r.reason != "" {
		attrs = append(attrs, "reason", r.reason)
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
